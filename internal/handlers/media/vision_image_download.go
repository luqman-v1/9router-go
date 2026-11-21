package media

import (
	"net/http"
	"sync"
	"time"

	"9router/proxy/internal/proxy"
)

// visionImageDownloadClient is the HTTP client the vision bridge uses to pull a
// caller-supplied remote image (the Cloudflare reference/mask fetch). Its
// transport judges the address each socket actually connects to, so a hostname
// that resolves public at a pre-flight check and private at dial time (DNS
// rebinding) or a redirect to loopback / RFC1918 / cloud metadata
// (169.254.169.254) cannot be reached.
//
// It is deliberately NOT the shared media client: a caller-supplied image URL is
// the SSRF surface, so the connect-time guard is unconditional and does not
// consult ALLOW_PRIVATE_NETWORKING. Redirects still go through
// checkRedirectPublic, which judges each hop's URL the same way.
//
// The transport is proxy.NewStrictSafeTransport: Proxy from the environment and
// a dialer that exempts the ambient proxy endpoint, but refuses every
// private/reserved target regardless of ALLOW_PRIVATE_NETWORKING. An operator's
// egress proxy (a local CONNECT proxy, a sandbox sidecar, the .env values
// syncEnvFromViper lifts) keeps working instead of being refused as an SSRF
// target; a caller-supplied URL cannot reach loopback / RFC1918 / metadata even
// under the default ALLOW_PRIVATE_NETWORKING=true.
var visionImageDownloadClient = sync.OnceValue(func() *http.Client {
	transport := proxy.NewStrictSafeTransport()
	transport.ResponseHeaderTimeout = 15 * time.Second
	return &http.Client{
		Timeout:       30 * time.Second,
		Transport:     transport,
		CheckRedirect: checkRedirectPublic,
	}
})
