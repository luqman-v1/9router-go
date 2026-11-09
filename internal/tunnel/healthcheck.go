package tunnel

import (
	"context"
	"fmt"
	net "net"
	"net/http"
	neturl "net/url"
	"time"

	"9router/proxy/internal/log"
)

// Health probe timings. The quick tunnel's DNS propagates in seconds, so the
// poll is tight and the budget generous: a tunnel that has not come up in 60s
// is not coming up, and waiting longer only holds the dashboard's spinner.
//
// healthTimeout is a variable rather than a constant so a test can pin it: the
// real budget is a minute, and a unit test that waits out a real minute to
// prove a cancellation path is a minute of CI spent on nothing.
var healthTimeout = 60 * time.Second

const (
	healthInterval = 2 * time.Second
	healthFetch    = 5 * time.Second
	healthDNS      = 2 * time.Second
)

// publicDNSServers are queried before the OS resolver. The OS negative cache
// (mDNSResponder on macOS, systemd-resolved on Linux) holds an NXDOMAIN for a
// freshly minted *.trycloudflare.com host, so a tunnel that is actually up
// reads as unreachable until that entry expires.
var publicDNSServers = []string{"1.1.1.1:53", "1.0.0.1:53", "8.8.8.8:53"}

// resolveDNS reports whether hostname resolves, trying the public resolvers
// first and the OS resolver last. A hostname only some resolvers know (a
// *.ts.net name, say) still resolves through the fallback.
func resolveDNS(ctx context.Context, hostname string) bool {
	for _, server := range publicDNSServers {
		if probeResolver(ctx, server, hostname) {
			return true
		}
	}
	return probeResolver(ctx, "", hostname)
}

func probeResolver(ctx context.Context, server, hostname string) bool {
	ctx, cancel := context.WithTimeout(ctx, healthDNS)
	defer cancel()

	var r *net.Resolver
	if server == "" {
		r = net.DefaultResolver
	} else {
		r = &net.Resolver{
			PreferGo: true,
			Dial: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(dialCtx, "udp", server)
			},
		}
	}

	addrs, err := r.LookupHost(ctx, hostname)
	return err == nil && len(addrs) > 0
}

// hostFromURL extracts the hostname a probe should resolve. It is a helper
// rather than an inline url.Parse because every caller needs the same
// tolerance for a URL missing its scheme.
func hostFromURL(rawURL string) (string, error) {
	u, err := neturl.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("tunnel.hostFromURL: %w", err)
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("tunnel.hostFromURL: %q has no host", rawURL)
	}
	return u.Hostname(), nil
}

// healthPaths are probed in order, and the first one that answers 2xx wins.
//
// The gateway serves `/health` (internal/handlers/router.go, SetupServerRouter),
// while the upstream Next.js implementation serves `/api/health`. Both are
// tried because this binary can sit behind a proxy that rewrites one to the
// other, and because a tunnel created against upstream 9router points at a
// gateway whose route table this package does not control. Probing only one of
// them is what made a working tunnel read as dead: the process was up, the URL
// resolved, and the probe got a 404 for a path the origin never served.
var healthPaths = []string{"/health", "/api/health"}

// LocalPortAlive reports whether something is accepting connections on a local
// port.
//
// It exists for the Tailscale staleness check, which has to tell a port the
// gateway moved off from one it still listens on. The daemon's own status names
// the funnel but never the origin behind it, so the only way to know is to
// knock on the door.
func LocalPortAlive(ctx context.Context, port int) bool {
	if port <= 0 {
		return false
	}
	d := net.Dialer{Timeout: 2 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// ProbeURLAlive reports whether the gateway answers through url.
func ProbeURLAlive(ctx context.Context, url string) bool {
	if url == "" {
		return false
	}
	host, err := hostFromURL(url)
	if err != nil {
		return false
	}
	if !resolveDNS(ctx, host) {
		// A DNS failure is the most common reason a fresh tunnel reads as
		// dead, and it is invisible without this line: the URL is well-formed,
		// the process is up, and nothing in the error says "the hostname did
		// not resolve".
		log.Debug("tunnel", "health probe: DNS did not resolve", "host", host)
		return false
	}

	for _, path := range healthPaths {
		if probePath(ctx, url, path) {
			return true
		}
	}
	log.Debug("tunnel", "health probe: no health path answered", "url", url)
	return false
}

// probePath requests one health path. Each attempt gets its own timeout so a
// hanging first path cannot consume the budget the second one needs.
func probePath(ctx context.Context, baseURL, path string) bool {
	ctx, cancel := context.WithTimeout(ctx, healthFetch)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
	if err != nil {
		return false
	}
	resp, err := healthClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// healthClient is shared so repeated probes reuse connections instead of
// paying a TCP handshake per tick.
var healthClient = &http.Client{
	Timeout: healthFetch,
	// A tunnel URL redirects once at most; following it is correct here
	// because the redirect target is still the operator's own gateway.
	CheckRedirect: func(*http.Request, []*http.Request) error { return nil },
}

// WaitForHealth polls until the URL answers or the budget runs out. It is the
// difference between "cloudflared started" and "the tunnel actually carries
// traffic" — the process can be alive for many seconds before the edge routes
// to it.
func WaitForHealth(ctx context.Context, url string, cancelled func() bool) error {
	deadline := time.Now().Add(healthTimeout)
	for time.Now().Before(deadline) {
		if cancelled != nil && cancelled() {
			return fmt.Errorf("tunnel.WaitForHealth: cancelled")
		}
		if ProbeURLAlive(ctx, url) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("tunnel.WaitForHealth: %w", ctx.Err())
		case <-time.After(healthInterval):
		}
	}
	return fmt.Errorf("tunnel.WaitForHealth: %s did not become healthy within %ds", url, int(healthTimeout.Seconds()))
}
