package executor

import (
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

// geminiWebRotateTimeout bounds one rotation call. The endpoint answers in
// well under a second, so a longer wait means the call is not going to
// happen.
const geminiWebRotateTimeout = 20 * time.Second

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
	// origin is the site this session belongs to. The session's cookies are
	// copied from here when another host has to carry them.
	origin string
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
func newGeminiWebStealthClient(reference *http.Client, origin, credential string) (*geminiWebStealthClient, error) {
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
	stack := &geminiWebStealthClient{client: client, origin: origin}
	// The credential is seeded once, here, and never again: the jar is the one
	// place a cookie's current value lives. Re-seeding on each call would put the
	// value the user pasted back over one the site has since rotated.
	stack.seedCredential(origin, credential)
	return stack, nil
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
//
// The credential is seeded into the jar rather than written as a Cookie header,
// and the header is stripped before the call. Both halves are needed: fhttp
// inserts jar cookies with AddCookie, so a hand-written header is not replaced
// but appended to — one name, two values, the stale one first. The site rotates
// __Secure-1PSIDTS and treats a stale value as a cancelled session, so the
// appended duplicate is what turns a signed-in session into an anonymous one
// without any error to show for it.
//
// Seeding on every call keeps the jar's non-rotated entries in step with the
// configured credential, and lets the rotated ones — which only the jar has —
// survive.
func (s *geminiWebStealthClient) do(ctx *fhttp.Request) (*fhttp.Response, error) {
	// The Cookie header is left empty on purpose: the jar writes the cookie, and
	// it holds whatever the site last issued. A header written here would be
	// appended to rather than replaced, sending the name twice.
	ctx.Header.Del("cookie")
	return s.client.Do(ctx)
}

// seedCredential puts the configured credential into the jar for one host.
func (s *geminiWebStealthClient) seedCredential(origin, credential string) {
	if strings.TrimSpace(origin) == "" || strings.TrimSpace(credential) == "" {
		return
	}
	u, err := url.Parse(origin)
	if err != nil {
		return
	}
	cookies := make([]*fhttp.Cookie, 0, 8)
	for _, part := range strings.Split(credential, ";") {
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
		s.client.SetCookies(u, cookies)
	}
}

// geminiWebSessionCookieNames are the Google session cookies the site's own
// client carries to the content-push service.
//
// The reference bridge sends the whole header it holds
// (azrinaldi/gemini-stealth-bridge internal/cookies/jar.go, EssentialNames),
// and this is that list. A cookie outside it is not a session cookie, and
// handing one to a second host is handing away something the user did not
// mean to share.
var geminiWebSessionCookieNames = map[string]bool{
	"SID": true, "HSID": true, "SSID": true, "APISID": true, "SAPISID": true,
	"__Secure-1PSID": true, "__Secure-1PAPISID": true,
	"__Secure-3PSID": true, "__Secure-3PAPISID": true,
	"__Secure-1PSIDTS": true, "__Secure-3PSIDTS": true,
	"SIDCC": true, "__Secure-1PSIDCC": true, "__Secure-3PSIDCC": true,
	"NID": true, "AEC": true,
}

// geminiWebSessionCarryingCookies are the cookies a renewal has to move for
// the session to have been renewed.
//
// The session is carried by the session id and its timestamp: a header
// without them is answered as an anonymous visitor's. The other cookies the
// site issues — `NID` above all — are refreshed on every answer, including a
// refusal, so they say nothing about whether a renewal happened.
var geminiWebSessionCarryingCookies = map[string]bool{
	"__Secure-1PSID":   true,
	"__Secure-1PSIDTS": true,
	"__Secure-3PSID":   true,
	"__Secure-3PSIDTS": true,
}

// shareCredentialTo copies the session's cookies to another host.
//
// The content-push service the image is uploaded to is a different host from
// the site, and Google's own composer carries the session cookie across to
// it. A jar is per-host, so the site's cookies are not there to be sent: an
// upload made without them belongs to no session, and a signed-in account
// whose upload is anonymous gets an attachment the model cannot resolve.
//
// The values are read back out of the jar rather than from the configured
// credential, so a cookie the site has since rotated is the one that travels.
func (s *geminiWebStealthClient) shareCredentialTo(target string) {
	if s.origin == "" || strings.TrimSpace(target) == "" {
		return
	}
	source, err := url.Parse(s.origin)
	if err != nil {
		return
	}
	destination, err := url.Parse(target)
	if err != nil {
		return
	}
	if source.Host == destination.Host {
		return
	}
	shared := make([]*fhttp.Cookie, 0, 8)
	for _, cookie := range s.client.GetCookies(source) {
		if geminiWebSessionCookieNames[cookie.Name] {
			shared = append(shared, cookie)
		}
	}
	if len(shared) == 0 {
		return
	}
	s.client.SetCookies(destination, shared)
}

// postRotate issues the renewal request the site's own page makes.
//
// The endpoints are on accounts.google.com, so the request carries that
// origin and refers to that page rather than to the site's — a request that
// named gemini.google.com as its origin while posting to accounts.google.com
// is not a request the site made.
func (s *geminiWebStealthClient) postRotate(ctx context.Context, target, body string) (int, []*fhttp.Cookie, error) {
	requestCtx, cancel := context.WithTimeout(ctx, geminiWebRotateTimeout)
	defer cancel()
	request, err := fhttp.NewRequestWithContext(requestCtx, http.MethodPost, target, strings.NewReader(body))
	if err != nil {
		return 0, nil, fmt.Errorf("gemini-web: build rotation request: %w", err)
	}
	values := map[string]string{
		"content-type":   "application/json",
		"origin":         geminiWebRotateOrigin,
		"referer":        geminiWebRotateOrigin + "/",
		"sec-fetch-site": "same-origin",
		"user-agent":     geminiWebUserAgent,
	}
	geminiWebSetOrdered(request.Header, geminiWebCORSOrder, values)
	request.Header[fhttp.PHeaderOrderKey] = geminiWebPseudoHeaderOrder
	response, err := s.do(request)
	if err != nil {
		return 0, nil, fmt.Errorf("gemini-web: rotation request: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	return response.StatusCode, response.Cookies(), nil
}

// getRotatePage reads the page the browser's rotation iframe loads.
//
// Its body carries the rotation id the renew request has to quote, and the
// interval the site wants between renewals.
func (s *geminiWebStealthClient) getRotatePage(ctx context.Context) (int, []*fhttp.Cookie, string, error) {
	requestCtx, cancel := context.WithTimeout(ctx, geminiWebRotateTimeout)
	defer cancel()
	request, err := fhttp.NewRequestWithContext(requestCtx, http.MethodGet, geminiWebRotatePageURL, nil)
	if err != nil {
		return 0, nil, "", fmt.Errorf("gemini-web: build rotation page request: %w", err)
	}
	values := map[string]string{
		"user-agent":     geminiWebUserAgent,
		"accept":         "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
		"sec-fetch-site": "same-site",
		"sec-fetch-mode": "navigate",
		"sec-fetch-dest": "iframe",
		"referer":        geminiWebOrigin + "/",
	}
	geminiWebSetOrdered(request.Header, geminiWebNavigationOrder, values)
	request.Header[fhttp.PHeaderOrderKey] = geminiWebPseudoHeaderOrder
	response, err := s.do(request)
	if err != nil {
		return 0, nil, "", fmt.Errorf("gemini-web: rotation page: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return response.StatusCode, nil, "", fmt.Errorf("gemini-web: read rotation page: %w", err)
	}
	return response.StatusCode, response.Cookies(), string(body), nil
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
