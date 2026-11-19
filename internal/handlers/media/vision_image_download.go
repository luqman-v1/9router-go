package media

import (
	"context"
	"net"
	"net/http"
	"sync"
	"syscall"
	"time"

	"9router/proxy/internal/netguard"
	"9router/proxy/internal/proxy"
)

// visionImageDownloadClient is the HTTP client the vision bridge uses to pull a
// caller-supplied remote image (the Cloudflare reference/mask fetch and the
// Gemini Web remote-image fetch). It exists because the generic media client's
// socket-level guard is disabled by the default ALLOW_PRIVATE_NETWORKING=true:
// under that default a hostname that resolves public at the pre-flight check
// and private at dial time (DNS rebinding), or a redirect that lands on
// loopback / RFC1918 / cloud metadata (169.254.169.254), would otherwise be
// followed. OmniRoute #20475 closes the same gap by pinning the connect-time
// address on every hop.
//
// This client is deliberately NOT the shared media client and does not consult
// ALLOW_PRIVATE_NETWORKING: a caller-supplied image URL is the SSRF surface, so
// the connect-time check is unconditional. The pre-flight literal-IP check in
// resolveImageInput stays as a fast, clear refusal; this is the boundary that
// also covers the rebind between that lookup and the dial.
var visionImageDownloadClient = sync.OnceValue(func() *http.Client {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		ControlContext: func(_ context.Context, network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				host = address
			}
			// netguard.ParseIP strips an IPv6 zone; net.ParseIP answers nil for
			// "::1%lo" and the nil branch below would let the literal through.
			ip := netguard.ParseIP(host)
			if ip == nil {
				return nil
			}
			if netguard.IsPrivateOrReserved(ip) {
				return proxy.ErrSSRFBlocked
			}
			return nil
		},
	}
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 15 * time.Second,
			MaxIdleConns:          4,
			IdleConnTimeout:       30 * time.Second,
		},
		// Every hop re-checks the target address. checkRedirectPublic already
		// validates each redirect URL against the public-only policy; the
		// dialer's ControlContext then judges the resolved address the socket
		// actually uses, so a redirect to a name that resolves private is
		// refused at connect even when the URL itself looks public.
		CheckRedirect: checkRedirectPublic,
	}
})
