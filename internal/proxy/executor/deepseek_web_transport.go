package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// The transport chat.deepseek.com's own client is reached through.
//
// Why not net/http: Go writes its own TLS ClientHello and sorts request
// headers alphabetically before sending them. Chrome does neither. The site
// sits behind an AWS WAF, and the request has to look like the browser the
// site expects.
//
// What the WAF actually checks was measured, and it is worth stating
// precisely, because it decides how much of this is load-bearing:
//
//   - The document request without a cookie is answered with HTTP 202 and a
//     JavaScript challenge; with a cookie the site issued, it is answered
//     with the app. That is true over Go's TLS stack, over a plain
//     tls-client stack, and over a Chrome-profile stack alike. The WAF
//     decides on the cookie, not on the TLS fingerprint.
//   - The /api/v0/* endpoints do not challenge at all. They answer with the
//     same body whether the request carries no cookie, the site's own
//     cookie, or a fabricated one. The credential there is the Bearer token.
//
// So the TLS stack is defence in depth rather than the thing that earns the
// request, and the cookie is what the WAF reads. The stack is kept anyway:
// the site is a consumer product with anti-bot measures in front of it, and
// the request should look like the client it claims to be. This is the same
// reasoning, and the same pure-Go dependency, the Gemini Web and OpenCode
// providers already use (AGENTS.md section 4.F allows a pure-Go TLS library;
// a cgo one is still forbidden).
//
// This is pure Go — no cgo, and it cross-compiles to every target the
// release pipeline builds.

// deepSeekWebTLSProfile is the Chrome build the fingerprint is taken from. It
// is named once so the UA string and the TLS profile cannot drift apart: a
// request that claims Chrome 152 while its ClientHello says Chrome 150 is a
// request that contradicts itself.
//
// 152 is the newest Chrome the pure-Go TLS stack ships a profile for, so the
// UA claims that build rather than the one the capture happened to run.
var deepSeekWebTLSProfile = profiles.Chrome_152_PSK

// deepSeekTransport issues the provider's requests.
//
// The cookie is a real one the site's own browser received, and the jar is
// where it lives: the site rotates its WAF token, and a jar is the only
// thing that keeps the rotated value. Seeding the jar once and letting it
// hold the current value is what stops the router from sending a cookie the
// site has already replaced.
type deepSeekTransport struct {
	client tls_client.HttpClient
	origin string
}

// newDeepSeekTransport builds the transport for one connection.
//
// The transport handed in by the chat path already has the connection's
// proxy pool resolved into it, so the proxy is read back off it and given to
// the tls-client. Without that the operator's egress assignment would
// silently not apply to this provider — the request would leave from the
// host IP while the dashboard showed it as proxied.
func newDeepSeekTransport(reference *http.Client, origin, cookie string) (*deepSeekTransport, error) {
	options := []tls_client.HttpClientOption{
		tls_client.WithTimeoutSeconds(0),
		tls_client.WithClientProfile(deepSeekWebTLSProfile),
		// Chrome shuffles its TLS extension order per connection; a
		// fingerprint with a fixed order is itself distinctive.
		tls_client.WithRandomTLSExtensionOrder(),
		tls_client.WithCookieJar(tls_client.NewCookieJar()),
		tls_client.WithDialer(net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}),
		// The API answers JSON, not a redirect chain; following one would
		// hide the real response behind a login page.
		tls_client.WithNotFollowRedirects(),
	}
	if proxyURL := stealthProxyFrom(reference, origin); proxyURL != "" {
		options = append(options, tls_client.WithProxyUrl(proxyURL))
	}
	client, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(), options...)
	if err != nil {
		return nil, fmt.Errorf("deepseek-web: build TLS client: %w", err)
	}
	transport := &deepSeekTransport{client: client, origin: origin}
	transport.seedCookie(origin, cookie)
	return transport, nil
}

// seedCookie puts the configured cookie into the jar for the site's host.
//
// The cookie is seeded once, here, and never again: the jar is the one place
// a cookie's current value lives. Re-seeding on each call would put the
// value the operator pasted back over one the site has since rotated.
func (t *deepSeekTransport) seedCookie(origin, cookie string) {
	if strings.TrimSpace(origin) == "" || strings.TrimSpace(cookie) == "" {
		return
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return
	}
	cookies := make([]*fhttp.Cookie, 0, 8)
	for _, part := range strings.Split(cookie, ";") {
		name, value, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found || strings.TrimSpace(name) == "" {
			continue
		}
		cookies = append(cookies, &fhttp.Cookie{
			Name:  strings.TrimSpace(name),
			Value: strings.TrimSpace(value),
			Path:  "/",
		})
	}
	if len(cookies) > 0 {
		t.client.SetCookies(parsed, cookies)
	}
}

// do issues one request and answers with the response.
//
// The Cookie header is left empty on purpose: the jar writes the cookie, and
// it holds whatever the site last issued. A header written here would be
// appended to rather than replaced, sending the name twice — and the site
// reads the first value, which would be the stale one.
func (t *deepSeekTransport) do(ctx context.Context, request *fhttp.Request) (*fhttp.Response, error) {
	request.Header.Del("cookie")
	return t.client.Do(request)
}

// send issues one request and answers with the response in the shape the rest
// of the provider expects.
//
// The response is converted rather than kept in fhttp's shape because every
// caller in this package reads a *http.Response: the streaming path hands the
// body to the SSE reader, and the JSON path reads it into an envelope.
func (t *deepSeekTransport) send(ctx context.Context, method, target string, body []byte, values map[string]string) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := fhttp.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, fmt.Errorf("deepseek-web: build request: %w", err)
	}
	stealthSetOrdered(request.Header, deepSeekAPIHeaderOrder, values)
	request.Header[fhttp.PHeaderOrderKey] = stealthPseudoHeaderOrder

	response, err := t.do(ctx, request)
	if err != nil {
		return nil, err
	}
	return deepSeekHTTPResponse(response), nil
}

// deepSeekHTTPResponse converts a browser-stack response into the shape the
// shared code reads.
func deepSeekHTTPResponse(response *fhttp.Response) *http.Response {
	header := make(http.Header, len(response.Header))
	for name, values := range response.Header {
		header[name] = values
	}
	return &http.Response{
		StatusCode:    response.StatusCode,
		Status:        response.Status,
		Header:        header,
		Body:          response.Body,
		ContentLength: response.ContentLength,
	}
}

// deepSeekWebPlainHTTP reports whether the browser-fidelity stack is turned
// off.
//
// A switch exists because the fingerprint is an assumption about the site's
// defences, not a measured requirement: the API endpoints answer the same
// with and without it. If the extra stack ever misbehaves, the provider can
// be returned to the plain client without a code change.
func deepSeekWebPlainHTTP() bool {
	return os.Getenv("DEEPSEEK_WEB_PLAIN_HTTP") == "1"
}
