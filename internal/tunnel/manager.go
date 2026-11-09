package tunnel

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	net "net"
	"net/http"
	"net/netip"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/config"
	"9router/proxy/internal/log"
)

// workerBaseURL is the service that maps a shortId onto its tunnel URL, which
// is what makes the public URL stable while the quick tunnel's own hostname
// rotates. Overridable through the `tunnelWorkerUrl` setting so an operator
// can self-host the mapping service instead of depending on the default.
//
// It is a variable rather than read from settings on every call because
// registerURL runs on the tunnel's hot path — including on every URL rotation —
// and a settings read per registration would put a database query between the
// operator's tunnel and its public URL. The value is refreshed by
// SetWorkerURL when the operator changes it.
var workerBaseURL = "https://abc-tunnel.us"

// SetWorkerURL replaces the mapping service the tunnel registers with. An
// empty or whitespace value restores the default rather than disabling
// registration: a blank URL would silently break the public URL, and the
// operator's intent in clearing the field is "use the default", not "stop
// registering".
func SetWorkerURL(url string) {
	url = strings.TrimSpace(url)
	if url == "" {
		workerBaseURL = defaultWorkerURL
		return
	}
	workerBaseURL = strings.TrimSuffix(url, "/")
}

// defaultWorkerURL is the upstream mapping service.
const defaultWorkerURL = "https://abc-tunnel.us"

// WorkerURL reports the mapping service currently in use.
func WorkerURL() string { return workerBaseURL }

// Mode names persisted in state.json so a restart knows what to respawn.
const (
	ModeQuick = "quick"
	ModeNamed = "named"
	// ModeAuto provisions a named tunnel through the Cloudflare API: the
	// tunnel, its ingress rule and its DNS record are created from the
	// operator's credentials, so no manual dashboard step is left.
	ModeAuto = "auto"
)

// Options is what the operator configures for one tunnel.
type Options struct {
	// CustomShortID, when valid, becomes the tunnel's identity instead of a
	// random one. An invalid value falls back to a generated id rather than
	// failing the enable.
	CustomShortID string
	// Token enables named-tunnel mode: `cloudflared tunnel run --token`.
	// Empty means quick tunnel.
	Token string
	// Cloudflare carries the credentials for ModeAuto. When APIToken is set
	// the tunnel is provisioned through the Cloudflare API rather than run
	// from a pre-made token.
	Cloudflare CloudflareOptions
	// CustomDomain is the operator's own hostname for a named tunnel. It is
	// reported back so the dashboard can show the URL the operator expects.
	CustomDomain string
	// Protocol selects the quick-tunnel transport: http2, quic or auto.
	Protocol string
}

// TunnelStatus is the handler-facing view of the tunnel.
type TunnelStatus struct {
	Enabled         bool   `json:"enabled"`
	SettingsEnabled bool   `json:"settingsEnabled"`
	TunnelURL       string `json:"tunnelUrl"`
	ShortID         string `json:"shortId"`
	PublicURL       string `json:"publicUrl"`
	Running         bool   `json:"running"`
	Mode            string `json:"mode"`
	CustomDomain    string `json:"customDomain,omitempty"`
	// PreviousURL is the address the tunnel served before the current one, set
	// only when it actually rotated. A quick tunnel's hostname changes on every
	// reconnect, so the URL an operator wrote down can stop working while the
	// tunnel looks perfectly healthy — this is what lets the dashboard say so.
	PreviousURL string `json:"previousUrl,omitempty"`
	// RotatedAt is when the current URL replaced the previous one.
	RotatedAt time.Time `json:"rotatedAt,omitempty"`
	// State distinguishes "the connector process is alive" from "the endpoint
	// actually answers". The two differ often enough that collapsing them into
	// one boolean is what made a dead tunnel look connected: the process
	// survives an origin that points at the wrong port, and the health check
	// then times out with nothing in the dashboard to explain it.
	//
	// Values: "off" (not enabled), "connecting" (process alive, endpoint not
	// yet answering), "connected" (endpoint answering), "error" (enabled but no
	// live process).
	State string `json:"state"`
}

// EnableResult is returned to the dashboard after a successful enable.
type EnableResult struct {
	Success    bool   `json:"success"`
	TunnelURL  string `json:"tunnelUrl"`
	ShortID    string `json:"shortId"`
	PublicURL  string `json:"publicUrl"`
	Mode       string `json:"mode"`
	AlreadyRun bool   `json:"alreadyRunning,omitempty"`
}

// service holds the in-process tunnel state. It is a singleton because there
// is exactly one cloudflared per gateway: two would race for the same origin
// port and the same registered shortId.
type service struct {
	mu sync.Mutex

	// cancelToken is replaced on every enable and flipped by disable, so an
	// enable still waiting on a health probe cannot resurrect the tunnel
	// after the operator turned it off.
	cancelToken *cancelToken

	// spawnInProgress guards against a watchdog tick and an operator click
	// both deciding to start a tunnel.
	spawnInProgress bool

	lastRestartAt time.Time
	// lastTailscaleRestartAt is tracked separately so a Cloudflare restart
	// storm cannot consume the cooldown the Tailscale path needs, and vice
	// versa. One shared timestamp would let the first service to degrade
	// silence the other's recovery for two minutes.
	lastTailscaleRestartAt time.Time
	// tailscalePort is the port the funnel was last established on. The
	// daemon's own status reports that a funnel exists without naming its
	// origin, so without this the watchdog cannot tell a healthy funnel from
	// one still dialling a port the gateway no longer listens on.
	tailscalePort int
	activePort    int
}

type cancelToken struct {
	cancelled bool
}

var svc = &service{cancelToken: &cancelToken{}}

// IsReconnecting reports whether an enable is in flight. The dashboard uses it
// to show "reconnecting" rather than "stopped".
func IsReconnecting() bool {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	return svc.spawnInProgress
}

func (s *service) cancelled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancelToken.cancelled
}

// localPort is the origin cloudflared forwards to. The gateway's own port is
// the only correct answer: the tunnel exists to expose this process.
func localPort() int {
	if p := config.LoadConfig().Port; p > 0 {
		return p
	}
	return 20130
}

// Enable starts the tunnel and waits until it carries traffic.
//
// The order matters. Reuse is checked before anything is killed, so a tunnel
// that is already up and reachable is returned as-is instead of being torn
// down and rebuilt — which would rotate the quick-tunnel URL for no reason.
func Enable(ctx context.Context, opts Options) (*EnableResult, error) {
	port := localPort()

	svc.mu.Lock()
	if svc.spawnInProgress {
		svc.mu.Unlock()
		return nil, fmt.Errorf("tunnel.Enable: a tunnel start is already in progress")
	}
	token := &cancelToken{}
	svc.cancelToken = token
	svc.spawnInProgress = true
	svc.activePort = port
	svc.mu.Unlock()

	defer func() {
		svc.mu.Lock()
		svc.spawnInProgress = false
		svc.mu.Unlock()
	}()

	mode := ModeQuick
	switch {
	case strings.TrimSpace(opts.Cloudflare.APIToken) != "":
		mode = ModeAuto
	case strings.TrimSpace(opts.Token) != "":
		mode = ModeNamed
	}
	log.Info("tunnel", "enable start", "mode", mode, "port", port)

	bin, err := EnsureCloudflared(ctx)
	if err != nil {
		log.Error("tunnel", "enable failed: no cloudflared binary", "error", err)
		return nil, err
	}

	// The operator's own id wins when they typed one. The stored id is only a
	// fallback for the case where they left the field empty, so a tunnel that
	// was started with a random id keeps that id across restarts instead of
	// rotating its public URL on every toggle.
	//
	// The previous order was the reverse — the stored id always won — which
	// made the field look ignored: an operator who typed a shortId got the one
	// from an earlier run back, with nothing in the UI to explain why.
	shortID := ResolveShortID(opts.CustomShortID)
	if strings.TrimSpace(opts.CustomShortID) == "" {
		if st := LoadState(); st != nil && st.ShortID != "" {
			shortID = st.ShortID
		}
	}
	// Reuse only when the process is alive AND the URL still answers AND the
	// mode matches AND the shortId matches. A live process with a dead URL is a
	// tunnel that lost its edge connection, and reusing it would hand the
	// dashboard an endpoint that returns nothing. A live process in a
	// *different* mode must not be reused either: the operator asked for a
	// different tunnel, and returning the old one would silently ignore the
	// request.
	//
	// The shortId comparison is the same rule applied to the identity. The
	// mapping service keys on it, so a tunnel still running under the old id
	// keeps serving the old public URL whatever the operator typed — which is
	// why changing the field appeared to do nothing at all.
	if IsRunning() {
		if st := LoadState(); st != nil && st.TunnelURL != "" && st.Mode == mode && st.ShortID == shortID {
			public := publicURL(st.ShortID)
			if ProbeURLAlive(ctx, st.TunnelURL) && (mode == ModeNamed || mode == ModeAuto || ProbeURLAlive(ctx, public)) {
				log.Info("tunnel", "reusing running tunnel", "url", st.TunnelURL, "shortId", st.ShortID, "mode", mode)
				return &EnableResult{
					Success:    true,
					TunnelURL:  st.TunnelURL,
					ShortID:    st.ShortID,
					PublicURL:  public,
					Mode:       mode,
					AlreadyRun: true,
				}, nil
			}
		}

		// A mode or identity change is the case the reuse guard above
		// deliberately lets through. The old connector has to go first: two
		// cloudflared processes on the same origin fight over the connection,
		// and the one left running would keep serving the URL the operator is
		// moving away from.
		if st := LoadState(); st != nil && (st.Mode != mode || st.ShortID != shortID) {
			log.Info("tunnel", "restarting tunnel: configuration changed",
				"fromMode", st.Mode, "toMode", mode,
				"fromShortId", st.ShortID, "toShortId", shortID)
		}
	}

	// Anything still holding the port has to go before the new tunnel binds.
	Kill(port)
	if token.cancelled {
		return nil, fmt.Errorf("tunnel.Enable: cancelled")
	}

	// ModeAuto provisions the tunnel through the Cloudflare API first: the
	// connector token only exists once the tunnel, its ingress rule and its
	// DNS record do. A failure here is reported before anything is spawned, so
	// the operator fixes credentials rather than debugging a connector that
	// was never given a tunnel to join.
	if mode == ModeAuto {
		connToken, _, provErr := ProvisionNamedTunnel(ctx, opts.Cloudflare, port)
		if provErr != nil {
			log.Error("tunnel", "cloudflare provisioning failed", "hostname", opts.Cloudflare.Hostname, "error", provErr)
			return nil, fmt.Errorf("tunnel.Enable: provision named tunnel: %w", provErr)
		}
		opts.Token = connToken
		log.Info("tunnel", "cloudflare tunnel provisioned", "hostname", opts.Cloudflare.Hostname)
	}

	var tunnelURL string
	if mode == ModeNamed || mode == ModeAuto {
		tunnelURL = normaliseDomain(opts.CustomDomain)

		// A named tunnel is a connector joining a tunnel that already exists,
		// so there is no URL to discover in its output. Its readiness is the
		// operator's own hostname answering, which the health check below
		// confirms.
		res, spawnErr := spawnFn(bin, SpawnOptions{
			QuickTunnel: false,
			Token:       opts.Token,
		})
		if spawnErr != nil {
			return nil, fmt.Errorf("tunnel.Enable: run named tunnel connector: %w", spawnErr)
		}
		log.Info("tunnel", "named tunnel connector started", "pid", res.Cmd.Process.Pid, "hostname", tunnelURL)
	} else {
		cfgPath, cleanup, cfgErr := writeQuickTunnelConfig()
		if cfgErr != nil {
			return nil, cfgErr
		}
		defer cleanup()

		res, spawnErr := spawnFn(bin, SpawnOptions{
			QuickTunnel: true,
			Origin:      fmt.Sprintf("http://127.0.0.1:%d", port),
			Protocol:    normaliseProtocol(opts.Protocol),
			ConfigPath:  cfgPath,
			OnURL: func(url string) {
				// A rotated URL must be re-registered, or the stable public
				// URL keeps pointing at the edge that no longer serves it.
				if token.cancelled {
					return
				}
				// RecordTunnelURL rather than SaveState: it keeps the address
				// the tunnel served before this one, which is the only way the
				// dashboard can tell the operator that the URL they handed out
				// has rotated out from under them.
				_ = RecordTunnelURL(url)
				registerURL(shortID, url)
			},
		})
		if spawnErr != nil {
			return nil, fmt.Errorf("tunnel.Enable: %w", spawnErr)
		}
		tunnelURL = res.TunnelURL
	}

	// A named tunnel has no worker alias: the operator's own hostname IS the
	// endpoint, so that is what gets probed. Probing the worker URL instead
	// would wait out the full budget against a hostname this tunnel never
	// registered, then report a failure on a tunnel that was already serving.
	public := publicURL(shortID)
	probeURL := public
	if mode == ModeNamed || mode == ModeAuto {
		probeURL = tunnelURL
	}

	// Preflight the hostname before the health wait, so a misconfigured one is
	// reported in seconds rather than after the full budget. The two failures
	// that matter are indistinguishable from a timeout: DNS that does not
	// resolve at all, and DNS that resolves to something that is not
	// Cloudflare — which is what a hostname routed at the wrong target, or
	// still pointing at an old tunnel, looks like.
	if mode == ModeNamed || mode == ModeAuto {
		if diag := diagnoseHostname(ctx, probeURL); diag != "" {
			log.Warn("tunnel", "hostname preflight", "hostname", probeURL, "finding", diag)
		}
	}

	if err := SaveState(&State{ShortID: shortID, TunnelURL: tunnelURL, Mode: mode}); err != nil {
		return nil, err
	}
	// Registration is a quick-tunnel concern only: it maps a shortId onto a
	// rotating trycloudflare hostname. A named tunnel's hostname is fixed, so
	// there is nothing to map and nothing to re-map on reconnect.
	if mode == ModeQuick {
		registerURL(shortID, tunnelURL)
	}

	if err := WaitForHealth(ctx, probeURL, func() bool { return token.cancelled }); err != nil {
		if mode == ModeQuick && ProbeURLAlive(ctx, tunnelURL) {
			log.Warn("tunnel", "public URL not ready but the tunnel itself answers; accepting",
				"publicUrl", public, "tunnelUrl", tunnelURL)
		} else {
			// The health failure is the one error an operator cannot diagnose
			// from the UI alone: the tunnel process is up, the URL resolved,
			// and the only missing piece is which of those actually failed.
			// Every input to the probe is logged so the console says which.
			log.Error("tunnel", "health check failed",
				"probeUrl", probeURL,
				"publicUrl", public,
				"tunnelUrl", tunnelURL,
				"shortId", shortID,
				"mode", mode,
				"error", err)

			// A connector whose ingress rule points at a port this gateway is
			// not listening on produces exactly this timeout, and nothing in
			// the timeout says so. The connector's own output does, so it is
			// consulted here and the finding is returned to the operator
			// instead of leaving them to read a raw log.
			if mismatch, ok := detectOriginMismatch(outputTail.snapshot(), port); ok {
				log.Error("tunnel", "origin service points at the wrong port",
					"configuredOrigin", mismatch.Configured,
					"gatewayPort", mismatch.Expected)
				return nil, fmt.Errorf("tunnel.Enable: the tunnel's origin service is %s but this gateway listens on port %d — update the ingress rule in the Cloudflare dashboard, or use the Managed Tunnel method, which writes it for you",
					mismatch.Configured, mismatch.Expected)
			}
			return nil, err
		}
	} else {
		log.Info("tunnel", "endpoint healthy", "url", probeURL, "mode", mode)
	}
	if mode == ModeQuick && tunnelURL != "" {
		// Best-effort: a slow direct URL must not fail an enable that is
		// already reachable through the public one.
		_ = ProbeURLAlive(ctx, tunnelURL)
	}

	log.Info("tunnel", "enable success", "tunnelUrl", tunnelURL, "publicUrl", public, "shortId", shortID, "mode", mode)
	return &EnableResult{
		Success:   true,
		TunnelURL: tunnelURL,
		ShortID:   shortID,
		PublicURL: public,
		Mode:      mode,
	}, nil
}

// Disable stops the tunnel and clears its URL while keeping the identity.
func Disable() error {
	log.Info("tunnel", "disable start")
	svc.mu.Lock()
	svc.cancelToken.cancelled = true
	port := svc.activePort
	svc.activePort = 0
	svc.mu.Unlock()

	Kill(port)
	if err := ClearTunnelURL(); err != nil {
		return err
	}

	svc.mu.Lock()
	svc.spawnInProgress = false
	svc.mu.Unlock()
	return nil
}

// Status reports the tunnel as the dashboard sees it.
//
// settingsURL is the URL the handler persisted on the last successful enable.
// It is the fallback for the state file rather than the primary source: the
// file is what the running process wrote, but a tunnel enabled by an earlier
// version of this binary — or by the upstream Node implementation, which
// shares the data dir — has a settings row and no state file this package
// recognises.
//
// Running is a process probe, not a settings flag: a stored URL with no live
// process is a dead tunnel, and reporting it as running would send the
// operator's traffic into nothing.
func Status(settingsEnabled bool, settingsURL, customDomain string) TunnelStatus {
	st := LoadState()
	mode := ModeQuick
	shortID := ""
	tunnelURL := ""
	if st != nil {
		mode = st.Mode
		if mode == "" {
			mode = ModeQuick
		}
		shortID = st.ShortID
		tunnelURL = st.TunnelURL
	}
	if tunnelURL == "" {
		tunnelURL = settingsURL
	}

	running := false
	if settingsEnabled {
		running = IsRunning()
	}

	public := ""
	if shortID != "" {
		public = publicURL(shortID)
	}
	if mode == ModeNamed || mode == ModeAuto {
		// A named tunnel's endpoint is the operator's hostname. Reporting the
		// worker alias alongside it would offer a URL that resolves to
		// nothing, so the custom domain replaces it rather than sitting next
		// to it.
		if customDomain != "" {
			tunnelURL = normaliseDomain(customDomain)
		}
		public = tunnelURL
	}

	return TunnelStatus{
		Enabled:         settingsEnabled && running,
		SettingsEnabled: settingsEnabled,
		TunnelURL:       tunnelURL,
		ShortID:         shortID,
		PublicURL:       public,
		Running:         running,
		Mode:            mode,
		CustomDomain:    customDomain,
		PreviousURL:     st.previousURL(),
		RotatedAt:       st.rotatedAt(),
		State:           connectionState(settingsEnabled, running, tunnelURL),
	}
}

// connectionState names the stage the tunnel is in, for a dashboard that has to
// tell an operator what to wait for versus what to fix.
//
// "connecting" is the state that used to be invisible: the connector process is
// up, so the dashboard said connected, but the endpoint was still unreachable —
// DNS propagating, the edge still routing to a previous connection. Showing
// "connected" there sends the operator to debug a client that was never broken.
func connectionState(settingsEnabled, running bool, url string) string {
	if !settingsEnabled {
		return "off"
	}
	if !running {
		return "error"
	}
	if url == "" {
		return "connecting"
	}
	if ProbeURLAlive(context.Background(), url) {
		return "connected"
	}
	return "connecting"
}

// diagnoseHostname reports why a named tunnel's hostname is unlikely to answer,
// or "" when it looks fine. It runs before the health wait so the operator gets
// a cause in seconds instead of a timeout after a minute.
//
// The checks are deliberately about the *name*, not the tunnel: a connector
// that is up and a hostname that resolves can still be unrelated to each other,
// and that is the case a timeout cannot distinguish.
func diagnoseHostname(ctx context.Context, rawURL string) string {
	host, err := hostFromURL(rawURL)
	if err != nil {
		return "hostname could not be parsed: " + err.Error()
	}
	if !resolveDNS(ctx, host) {
		return "hostname does not resolve — the DNS record is missing, or it has not propagated yet"
	}

	// A hostname that resolves but is not on Cloudflare is the signature of a
	// record pointing somewhere else: an old server, a parked domain, or a
	// CNAME at the wrong target.
	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil || len(addrs) == 0 {
		return ""
	}
	for _, addr := range addrs {
		ip, parseErr := netip.ParseAddr(strings.TrimSpace(addr))
		if parseErr != nil {
			continue
		}
		if isCloudflareIP(ip.Unmap()) {
			return ""
		}
	}
	return "hostname resolves but not to Cloudflare — the DNS record points somewhere else, so the tunnel will never receive the traffic"
}

// cloudflareRanges are the published IP ranges Cloudflare serves from. A
// hostname that resolves outside them is not being proxied by Cloudflare, which
// is what a misrouted record looks like.
var cloudflareRanges = func() []netip.Prefix {
	cidrs := []string{
		"173.245.48.0/20",
		"103.21.244.0/22",
		"103.22.200.0/22",
		"103.31.4.0/22",
		"141.101.64.0/18",
		"108.162.192.0/18",
		"190.93.240.0/20",
		"188.114.96.0/20",
		"197.234.240.0/22",
		"198.41.128.0/17",
		"162.158.0.0/15",
		"104.16.0.0/13",
		"104.24.0.0/14",
		"172.64.0.0/13",
		"131.0.72.0/22",
	}
	out := make([]netip.Prefix, 0, len(cidrs))
	for _, c := range cidrs {
		if p, err := netip.ParsePrefix(c); err == nil {
			out = append(out, p)
		}
	}
	return out
}()

func isCloudflareIP(ip netip.Addr) bool {
	for _, p := range cloudflareRanges {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// publicURL is the stable hostname the worker serves for a shortId.
//
// The `r` prefix is not decoration: the mapping worker is keyed on it. Verified
// against the live service — `https://r<id>.abc-tunnel.us/health` answers 200
// while `https://<id>.abc-tunnel.us/health` answers 404. Dropping the prefix
// would produce a public URL that resolves to nothing, so it stays.
func publicURL(shortID string) string {
	if shortID == "" {
		return ""
	}
	return "https://r" + shortID + ".abc-tunnel.us"
}

// normaliseDomain turns an operator's hostname into a bare https:// origin.
//
// The port is stripped on purpose. A public hostname on Cloudflare is served on
// 443 regardless of what the operator typed, so a hostname carrying a port —
// "ai.helmiau.my.id:8080", usually a leftover from the local address — would
// otherwise produce a URL the health probe cannot reach. The probe would then
// fail for a full minute against a tunnel that is actually up, and the error
// would name a port nobody configured on the Cloudflare side.
//
// A path is dropped too: only the host is meaningful for a probe, and a
// hostname field that somehow carries one is a paste error worth ignoring
// rather than propagating into every URL the dashboard shows.
func normaliseDomain(domain string) string {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return ""
	}
	if !strings.Contains(domain, "://") {
		domain = "https://" + domain
	}
	u, err := neturl.Parse(domain)
	if err != nil || u.Hostname() == "" {
		// Unparseable input is passed through untouched rather than guessed
		// at: the health check will fail with the operator's own text in the
		// error, which is more useful than a silently rewritten hostname.
		return domain
	}
	return "https://" + u.Hostname()
}

// normaliseProtocol falls back to http2 for anything unrecognised. cloudflared
// rejects an unknown --protocol value outright, so a typo must not become a
// spawn failure.
func normaliseProtocol(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "quic":
		return "quic"
	case "auto":
		return "auto"
	default:
		return "http2"
	}
}

// writeQuickTunnelConfig creates the placeholder config cloudflared expects.
//
// The temp directory is deliberate: cloudflared reads ~/.cloudflared/config.yml
// by default, and a named-tunnel config sitting there would silently turn a
// quick tunnel into something else.
func writeQuickTunnelConfig() (string, func(), error) {
	dir, err := os.MkdirTemp("", "9router-quick-tunnel-")
	if err != nil {
		return "", nil, fmt.Errorf("tunnel.writeQuickTunnelConfig: %w", err)
	}
	path := filepath.Join(dir, "config.yml")
	content := "# quick-tunnel placeholder — do not add ingress rules here\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, fmt.Errorf("tunnel.writeQuickTunnelConfig: write: %w", err)
	}
	return path, func() { _ = os.RemoveAll(dir) }, nil
}

// registerURL maps a shortId onto its current tunnel URL at the worker. It is
// best-effort: the tunnel works without it, only the stable public URL does
// not, so a failure here must not abort an otherwise healthy enable.
//
// Every failure is logged, because a silent one is indistinguishable from a
// slow one at the health check: the probe waits out its full budget against a
// publicUrl that was never mapped, and the console shows only the timeout.
func registerURL(shortID, tunnelURL string) {
	if shortID == "" || tunnelURL == "" {
		return
	}
	body, err := json.Marshal(map[string]string{"shortId": shortID, "tunnelUrl": tunnelURL})
	if err != nil {
		log.Error("tunnel", "worker register: marshal failed", "shortId", shortID, "error", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, workerBaseURL+"/api/tunnel/register", bytes.NewReader(body))
	if err != nil {
		log.Error("tunnel", "worker register: request build failed", "shortId", shortID, "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := registerClient.Do(req)
	if err != nil {
		// This is the failure that costs the operator a minute: the publicUrl
		// is never mapped, so the health check polls it for its whole budget
		// and then reports a timeout with nothing pointing here.
		log.Error("tunnel", "worker register: request failed — publicUrl will not resolve",
			"shortId", shortID, "workerUrl", workerBaseURL, "error", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Error("tunnel", "worker register: non-2xx response — publicUrl will not resolve",
			"shortId", shortID, "workerUrl", workerBaseURL, "status", resp.Status)
		return
	}
	log.Info("tunnel", "worker register: shortId mapped", "shortId", shortID, "tunnelUrl", tunnelURL)
}

var registerClient = &http.Client{Timeout: 12 * time.Second}
