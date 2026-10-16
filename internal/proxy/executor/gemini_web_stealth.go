package executor

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// geminiWebStealthClient is the TLS/HTTP2 stack this provider dials the site
// with, instead of Go's net/http.
//
// Why not net/http: Go writes TLS ClientHello and HTTP/2 frames its own way, and
// it sorts request headers alphabetically before sending them. Chrome does
// neither. The site is a logged-in Google product with anti-bot measures in
// front of it, so the request is made to look like the browser the site expects:
// Chrome's TLS extension order and cipher suites (JA3/JA4), Chrome's HTTP/2
// pseudo-header order, and Chrome's header order.
//
// This is pure Go — no cgo, and it cross-compiles to every target the release
// pipeline builds. The earlier refusal of this dependency was wrong: it was
// justified by a constraint ("CGO-free, no new Go dependency") that does not bar
// a pure-Go TLS library, and that was asserted rather than tested.
type geminiWebStealthClient struct {
	client tls_client.HttpClient
}

// geminiWebTLSProfile is the Chrome build the fingerprint is taken from. It is
// named once so the UA string and the TLS profile cannot drift apart.
var geminiWebTLSProfile = profiles.Chrome_150_PSK

// newGeminiWebStealthClient builds the client for one session.
//
// The transport handed in by the chat path already has the connection's proxy
// pool resolved into it (handlers/chat/connections_proxy.go), so the proxy is
// read back off it and given to the tls-client. Without that the operator's
// egress assignment would silently not apply to this provider — the request
// would leave from the host IP while the dashboard showed it as proxied.
func newGeminiWebStealthClient(reference *http.Client) (*geminiWebStealthClient, error) {
	options := []tls_client.HttpClientOption{
		// The client's own overall timeout stays disabled: this provider streams,
		// and the two clocks that matter — first byte and idle — are enforced by
		// geminiWebLineReader, which can tell a slow answer from a stalled one.
		// An overall deadline here would cut a legitimately long answer short.
		tls_client.WithTimeoutSeconds(0),
		tls_client.WithClientProfile(geminiWebTLSProfile),
		// Chrome shuffles its TLS extension order per connection; a fingerprint
		// with a fixed order is itself distinctive.
		tls_client.WithRandomTLSExtensionOrder(),
		tls_client.WithCookieJar(tls_client.NewCookieJar()),
		tls_client.WithDialer(net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}),
		// The site answers a magic-prefixed line stream, not a redirect chain;
		// following one would hide the real response behind a login page.
		tls_client.WithNotFollowRedirects(),
	}
	if proxyURL := geminiWebProxyFrom(reference); proxyURL != "" {
		options = append(options, tls_client.WithProxyUrl(proxyURL))
	}
	client, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(), options...)
	if err != nil {
		return nil, fmt.Errorf("gemini-web: build TLS client: %w", err)
	}
	return &geminiWebStealthClient{client: client}, nil
}

// geminiWebProxyFrom recovers the proxy a connection was assigned.
//
// The chat path resolves the pool into the transport before the executor runs, so
// this reads it back rather than looking anything up: there is no second source
// of truth to disagree with. An unproxied connection yields "" and the client
// dials direct.
func geminiWebProxyFrom(reference *http.Client) string {
	if reference == nil {
		return ""
	}
	transport, ok := reference.Transport.(*http.Transport)
	if !ok || transport.Proxy == nil {
		return ""
	}
	// The proxy function is asked for a URL to a fixed placeholder target. It
	// resolves per scheme and host, and a pool's answer does not vary by the
	// upstream it is relaying to.
	target, err := url.Parse(geminiWebOrigin)
	if err != nil {
		return ""
	}
	proxyURL, err := transport.Proxy(&http.Request{URL: target})
	if err != nil || proxyURL == nil {
		return ""
	}
	return proxyURL.String()
}

// do issues one request through the stealth stack.
func (s *geminiWebStealthClient) do(ctx *fhttp.Request) (*fhttp.Response, error) {
	return s.client.Do(ctx)
}

// closeIdleConnections drops pooled connections whose TLS session may have been
// retired upstream.
func (s *geminiWebStealthClient) closeIdleConnections() {
	s.client.CloseIdleConnections()
}

// geminiWebHeadersDisabled reports whether the stealth stack is turned off.
//
// A switch exists because the fingerprint is an assumption about the site's
// defences, not a measured requirement: anonymous turns were served over plain
// net/http. If the extra stack ever misbehaves, the provider can be returned to
// the plain client without a code change.
func geminiWebHeadersDisabled() bool {
	return os.Getenv("GEMINI_WEB_PLAIN_HTTP") == "1"
}
