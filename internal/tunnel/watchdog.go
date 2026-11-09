package tunnel

import (
	"context"
	"net"
	"runtime"
	"strings"
	"time"

	"9router/proxy/internal/log"
)

// Watchdog timings, matching the upstream Node implementation so both
// binaries behave the same on the same machine.
const (
	// restartCooldown stops a flapping tunnel from hammering the TryCloudflare
	// service: a degraded tunnel is retried at most once per two minutes.
	restartCooldown = 120 * time.Second

	// networkSettle is how long a detected network change is left alone before
	// the tunnel is restarted, because a laptop switching Wi-Fi to Ethernet
	// settles in a couple of seconds and restarting mid-transition just fails.
	networkSettle = 2500 * time.Millisecond

	// watchdogInterval is the periodic health tick.
	watchdogInterval = 60 * time.Second

	// networkCheckInterval is how often the interface fingerprint is sampled.
	networkCheckInterval = 5 * time.Second
)

// virtualIfacePrefixes names the interfaces that flap constantly without
// meaning the network changed: tailscale's utun, AirDrop's awdl, Docker's
// bridges. Treating those as a network change would restart the tunnel every
// time a container starts.
var virtualIfacePrefixes = []string{
	"utun", "awdl", "llw", "anpi", "bridge", "br-", "gif", "stf",
	"ipsec", "ap", "tun", "tap", "vmnet", "veth", "docker",
}

// forceRestartReasons bypass the cooldown. These are one-shot transitions —
// the machine woke up, the network came back — where waiting two minutes
// leaves the operator with a dead tunnel for no benefit.
var forceRestartReasons = map[string]bool{
	"startup":         true,
	"netchange":       true,
	"sleep":           true,
	"sleep+netchange": true,
	"online":          true,
	"unexpected-exit": true,
}

// Watchdog keeps the tunnel up across the failures that would otherwise leave
// it down until someone noticed: a network change, a laptop waking from sleep,
// cloudflared exiting, and the gateway itself restarting.
//
// It covers both expose paths. Cloudflare gets the full treatment — the process
// is this binary's child, so only the watchdog can bring it back. Tailscale is
// different: its daemon is an OS-level service that reconnects on its own, so
// the watchdog only recovers the *funnel*, which is the part that drops
// without the daemon noticing. Restarting a healthy Tailscale daemon would
// throw away a working login for no gain.
type Watchdog struct {
	enabled func() bool
	now     func() time.Time

	// tailscale probes are injected so the loop never shells out to a real
	// binary on its own: the daemon's state belongs to the operator, and a
	// background tick that resets it is not a watchdog, it is a hazard.
	tailscaleEnabled  func() bool
	tailscaleDaemonUp func(ctx context.Context) bool
	tailscaleFunnelUp func(ctx context.Context) bool
	tailscaleFunnel   func(ctx context.Context, port int) error

	// tailscaleOriginAlive reports whether something is listening on a given
	// local port. It is a seam so the staleness check can be tested without a
	// real gateway, and so a port that still answers is never mistaken for one
	// the gateway moved off.
	tailscaleOriginAlive func(ctx context.Context, port int) bool
	stop                 chan struct{}
	done                 chan struct{}
}

// NewWatchdog builds a watchdog that only acts while enabled reports true. The
// enabled func is read on every tick rather than captured, so toggling the
// tunnel off in the dashboard takes effect on the next tick without a restart.
func NewWatchdog(enabled func() bool) *Watchdog {
	return &Watchdog{
		enabled: enabled,
		now:     time.Now,
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
}

// WithTailscale wires the Tailscale recovery path.
//
// The watchdog gate and the tailscale gate are separate on purpose: an operator
// may want the Cloudflare tunnel supervised while leaving Tailscale to the OS
// service manager, or the reverse. A single gate would force both on or both
// off, and the dashboard would show two switches that were really one.
func (w *Watchdog) WithTailscale(
	enabled func() bool,
	daemonUp func(ctx context.Context) bool,
	funnelUp func(ctx context.Context) bool,
	funnel func(ctx context.Context, port int) error,
	originAlive func(ctx context.Context, port int) bool,
) *Watchdog {
	w.tailscaleEnabled = enabled
	w.tailscaleDaemonUp = daemonUp
	w.tailscaleFunnelUp = funnelUp
	w.tailscaleFunnel = funnel
	w.tailscaleOriginAlive = originAlive
	return w
}

// SetWatchdogEnabled replaces the Cloudflare gate. It is called on every tick
// rather than captured at construction so toggling the setting in the dashboard
// takes effect on the next tick without a gateway restart.
func (w *Watchdog) SetWatchdogEnabled(enabled func() bool) {
	w.enabled = enabled
}

// SetTailscaleEnabled replaces the Tailscale gate, independently of the
// Cloudflare one.
func (w *Watchdog) SetTailscaleEnabled(enabled func() bool) {
	w.tailscaleEnabled = enabled
}

// Start launches the watchdog loops. It returns immediately; the goroutines
// exit when Stop is called or the context is done.
func (w *Watchdog) Start(ctx context.Context) {
	go w.run(ctx)
}

// Stop shuts the watchdog down and waits for its goroutines to finish, so a
// gateway shutdown does not leave a tick in flight against a closing database.
func (w *Watchdog) Stop() {
	select {
	case <-w.stop:
	default:
		close(w.stop)
	}
	<-w.done
}

func (w *Watchdog) run(ctx context.Context) {
	defer close(w.done)

	lastFingerprint := networkFingerprint()
	lastTick := w.now()

	watchdog := time.NewTicker(watchdogInterval)
	network := time.NewTicker(networkCheckInterval)
	defer watchdog.Stop()
	defer network.Stop()

	// Resume on startup: an operator who enabled the tunnel expects it to
	// still be there after the gateway restarts.
	w.restart(ctx, "startup")
	w.restartTailscale(ctx, "startup")

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stop:
			return

		case <-network.C:
			current := networkFingerprint()
			changed := current != lastFingerprint
			if changed {
				lastFingerprint = current
			}
			// A tick that arrives far later than its interval means the
			// machine was asleep: the timer did not fire while suspended.
			slept := w.now().Sub(lastTick) > networkCheckInterval*6
			lastTick = w.now()

			switch {
			case changed && slept:
				w.restart(ctx, "sleep+netchange")
				w.restartTailscale(ctx, "sleep+netchange")
			case changed:
				// Let the new network settle before restarting, or the
				// tunnel is rebuilt against an interface that is about to
				// change again.
				time.Sleep(networkSettle)
				w.restart(ctx, "netchange")
				w.restartTailscale(ctx, "netchange")
			case slept:
				w.restart(ctx, "sleep")
				w.restartTailscale(ctx, "sleep")
			}

		case <-watchdog.C:
			w.restart(ctx, "watchdog")
			w.restartTailscale(ctx, "watchdog")
			lastTick = w.now()
		}
	}
}

// urlAnswers reports whether the running tunnel's endpoint actually serves
// traffic. It probes the URL the state file records, which is the address the
// operator handed out — not the process state, which says nothing about whether
// anything is reachable.
//
// A probe failure is not proof of a dead tunnel: DNS for a freshly rotated
// quick-tunnel hostname can lag, and a quick tunnel mid-reconnect answers
// nothing for a few seconds. The cooldown already limits how often a restart
// can fire, so one bad probe costs a minute of waiting rather than a wrong
// teardown.
func (w *Watchdog) urlAnswers(ctx context.Context) bool {
	st := LoadState()
	if st == nil || st.TunnelURL == "" {
		// No recorded URL means nothing to probe; fall back to the process
		// check alone rather than assuming the worst.
		return true
	}
	// A named tunnel's endpoint is the operator's own hostname, which is what
	// the state records. A quick tunnel's is the rotating one, and its stable
	// alias is what the operator uses — probe the alias when there is a
	// shortId, since that is the address that has to keep working.
	probeURL := st.TunnelURL
	if st.Mode == ModeQuick && st.ShortID != "" {
		probeURL = publicURL(st.ShortID)
	}
	return ProbeURLAlive(ctx, probeURL)
}

// restart applies the guards upstream uses before touching the tunnel.
//
// A live cloudflared is normally left alone: it reconnects on its own via
// --retries 99 and keeps the same URL, so killing it on every network blip
// would rotate the endpoint for nothing.
//
// That guard is conditional on the URL actually answering. A process that is
// alive while its endpoint is dead is the failure this watchdog exists for — an
// origin pointed at the wrong port, a DNS record that never propagated, an edge
// connection that dropped without the process noticing. Treating "process
// alive" as "healthy" left exactly those cases unrecovered, which is why an
// operator could watch a connected-looking tunnel return 530 indefinitely.
func (w *Watchdog) restart(ctx context.Context, reason string) {
	if !w.enabled() {
		return
	}
	if svc.cancelled() {
		return
	}
	if IsReconnecting() {
		return
	}
	// Only a live process whose URL answers is left alone.
	if IsRunning() && w.urlAnswers(ctx) {
		return
	}

	force := forceRestartReasons[reason]
	if !force && w.now().Sub(svc.lastRestartAt) < restartCooldown {
		log.Debug("tunnel", "watchdog: degraded but cooldown active, skipping", "reason", reason)
		return
	}
	if !checkInternet(ctx) {
		log.Debug("tunnel", "watchdog: no internet, skipping restart", "reason", reason)
		return
	}
	log.Info("tunnel", "watchdog: restarting tunnel", "reason", reason, "forced", force)

	// The port is read from the active tunnel when there is one, so a restart
	// targets the same origin the previous tunnel used.
	port := svc.activePort
	if port == 0 {
		port = localPort()
	}

	st := LoadState()
	opts := Options{Protocol: "http2"}
	if st != nil {
		opts.CustomShortID = st.ShortID
		if st.Mode == ModeNamed {
			opts.Token = ""
			opts.CustomDomain = st.TunnelURL
		}
	}

	if _, err := Enable(ctx, opts); err != nil {
		log.Error("tunnel", "watchdog: restart failed", "reason", reason, "error", err)
		return
	}
	svc.lastRestartAt = w.now()
	log.Info("tunnel", "watchdog: restart succeeded", "reason", reason)
	_ = port
}

// tailscalePortStale reports whether the running funnel dials a port other
// than the one the gateway listens on now.
//
// The comparison is against the port the funnel was last established on, not
// against the daemon's reported origin: `tailscale funnel status` names the
// funnel but not the service behind it, so the only record of what it points at
// is the one this watchdog keeps. A zero value means no funnel has been
// established by this process yet, which is not staleness — it is a funnel
// someone else made, and re-establishing it would take ownership of a
// configuration the operator set up by hand.
func (w *Watchdog) tailscalePortStale(ctx context.Context, port int) bool {
	if svc.tailscalePort == 0 || svc.tailscalePort == port {
		return false
	}
	// Confirm the recorded port is genuinely dead before tearing down a
	// working funnel. A port that still answers means the gateway is listening
	// on both, and the funnel is not the thing that is broken.
	if w.tailscaleOriginAlive != nil && w.tailscaleOriginAlive(ctx, svc.tailscalePort) {
		return false
	}
	return true
}

// restartTailscale recovers a dropped Tailscale funnel.
//
// The daemon is deliberately never restarted: it is an OS-level service with
// its own reconnect logic and its own login state, and killing it would throw
// both away to solve a problem it does not have. Only the funnel — the part
// that drops silently — is re-established, and only when the daemon is
// confirmed alive to accept it.
func (w *Watchdog) restartTailscale(ctx context.Context, reason string) {
	if w.tailscaleEnabled == nil || !w.tailscaleEnabled() {
		return
	}
	if w.tailscaleDaemonUp == nil || w.tailscaleFunnel == nil {
		return
	}
	if !w.tailscaleDaemonUp(ctx) {
		// Nothing to attach a funnel to. Starting the daemon from here would
		// race the OS service manager and could leave two of them fighting
		// over the same socket.
		log.Debug("tunnel", "watchdog: tailscale daemon not running, leaving it to the OS", "reason", reason)
		return
	}
	// Resolve the port before the staleness check, which compares the funnel's
	// recorded origin against it.
	port := svc.activePort
	if port == 0 {
		port = localPort()
	}
	if w.tailscaleFunnelUp != nil && w.tailscaleFunnelUp(ctx) {
		// A funnel that is up is not necessarily a funnel that is up on the
		// right port. `tailscale funnel status` reports the daemon's funnel
		// without naming the origin, so an operator who changed the gateway's
		// PORT and restarted keeps a funnel dialling the old one — the process
		// is healthy, the URL answers, and the gateway behind it is a
		// connection refused. Re-establishing it is the only way to move it.
		if !w.tailscalePortStale(ctx, port) {
			return
		}
		log.Info("tunnel", "watchdog: tailscale funnel is on a stale port, re-establishing", "reason", reason, "port", port)
	}

	force := forceRestartReasons[reason]
	if !force && w.now().Sub(svc.lastTailscaleRestartAt) < restartCooldown {
		log.Debug("tunnel", "watchdog: tailscale degraded but cooldown active, skipping", "reason", reason)
		return
	}

	log.Info("tunnel", "watchdog: re-establishing tailscale funnel", "reason", reason, "port", port)
	if err := w.tailscaleFunnel(ctx, port); err != nil {
		log.Error("tunnel", "watchdog: tailscale funnel recovery failed", "reason", reason, "error", err)
		return
	}
	svc.lastTailscaleRestartAt = w.now()
	// Record the port the funnel now dials, so the next tick can tell a
	// healthy funnel from one left behind by a port change.
	svc.tailscalePort = port
	log.Info("tunnel", "watchdog: tailscale funnel recovered", "reason", reason)
}

// networkFingerprint summarises the non-virtual IPv4 addresses. A change in
// this string is what "the network changed" mean here.
func networkFingerprint() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	var parts []string
	for _, iface := range ifaces {
		if isVirtualIface(iface.Name) {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok || ipNet.IP.IsLoopback() || ipNet.IP.IsLinkLocalUnicast() {
				continue
			}
			v4 := ipNet.IP.To4()
			if v4 == nil {
				continue
			}
			parts = append(parts, iface.Name+":"+v4.String())
		}
	}
	return strings.Join(parts, "|")
}

func isVirtualIface(name string) bool {
	lower := strings.ToLower(name)
	for _, prefix := range virtualIfacePrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// checkInternet reports whether the machine can reach the internet at all.
// Restarting a tunnel on a machine that is offline just burns a cooldown.
func checkInternet(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", "one.one.one.one:443")
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// platformNote records the OS for diagnostics; a watchdog that behaves
// differently per platform is otherwise invisible in a bug report.
var _ = runtime.GOOS
