package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// grok.com's transport, ported from upstream open-sse/services/grokTlsClient.js
// (38a0c449, part of #624).
//
// Grok sits behind Cloudflare Enterprise, which rejects Go's native TLS
// fingerprint even with valid sso+sso-rw+cf_clearance cookies — upstream sends
// a Chrome-like handshake instead (browser profile chrome_146). This side uses
// the same pure-Go TLS stack the deepseek-web, gemini-web and opencode
// providers already use (AGENTS.md section 4.F), with the matching Chrome_146
// profile, so the ClientHello says the same build the User-Agent does — the
// pair upstream runs is chrome_146 under a Chrome/155 UA string, because the
// cf_clearance cookie only clears a request whose UA matches the browser that
// earned it.

// grokWebTLSProfile is the Chrome build the fingerprint is taken from.
var grokWebTLSProfile = profiles.Chrome_146

// grokWebUserAgent must match the UA of the browser the pasted cf_clearance
// was issued to (upstream GROK_TLS_USER_AGENT).
const grokWebUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/155.0.0.0 Safari/537.36"

// grokDoer issues one grok.com request. The interface exists because the test
// suite drives the whole flow against a fake site; production uses the
// browser-fidelity client below.
type grokDoer interface {
	do(ctx context.Context, method, target string, headers [][2]string, body []byte) (*http.Response, error)
}

// grokTransportFactory builds the doer for one origin. Var so the tests can
// swap in a plain client aimed at a fake site.
var grokTransportFactory = func(reference *http.Client, origin string) (grokDoer, error) {
	if grokWebPlainHTTP() {
		return &grokPlainDoer{}, nil
	}
	options := []tls_client.HttpClientOption{
		tls_client.WithTimeoutSeconds(0),
		tls_client.WithClientProfile(grokWebTLSProfile),
		// Chrome shuffles its TLS extension order per connection; a
		// fingerprint with a fixed order is itself distinctive.
		tls_client.WithRandomTLSExtensionOrder(),
		tls_client.WithNotFollowRedirects(),
		tls_client.WithDialer(net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}),
	}
	if proxyURL := stealthProxyFrom(reference, origin); proxyURL != "" {
		options = append(options, tls_client.WithProxyUrl(proxyURL))
	}
	client, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(), options...)
	if err != nil {
		return nil, fmt.Errorf("grok-web: build TLS client: %w", err)
	}
	return &grokTLSDoer{client: client}, nil
}

// grokTLSDoer runs requests through the impersonating client, writing the
// headers in the order given — header order is part of the fingerprint, and
// Go's net/http would sort them.
type grokTLSDoer struct {
	client tls_client.HttpClient
}

func (d *grokTLSDoer) do(ctx context.Context, method, target string, headers [][2]string, body []byte) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := fhttp.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, fmt.Errorf("grok-web: build request: %w", err)
	}
	values := make(map[string]string, len(headers))
	order := make([]string, 0, len(headers))
	for _, pair := range headers {
		values[pair[0]] = pair[1]
		if !slices.Contains(order, pair[0]) {
			order = append(order, pair[0])
		}
	}
	stealthSetOrdered(request.Header, order, values)
	request.Header[fhttp.PHeaderOrderKey] = stealthPseudoHeaderOrder

	response, err := d.client.Do(request)
	if err != nil {
		return nil, err
	}
	header := make(http.Header, len(response.Header))
	for name, vals := range response.Header {
		header[name] = vals
	}
	return &http.Response{
		StatusCode:    response.StatusCode,
		Status:        response.Status,
		Header:        header,
		Body:          response.Body,
		ContentLength: response.ContentLength,
	}, nil
}

// grokPlainDoer answers through net/http. It is the test and escape-hatch
// path: the fingerprint is an assumption about Cloudflare's checks, and if it
// ever misbehaves the provider can be returned to the plain client with
// GROK_WEB_PLAIN_HTTP=1, the same switch deepseek-web carries.
type grokPlainDoer struct{}

func (d *grokPlainDoer) do(ctx context.Context, method, target string, headers [][2]string, body []byte) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, fmt.Errorf("grok-web: build request: %w", err)
	}
	for _, pair := range headers {
		request.Header.Set(pair[0], pair[1])
	}
	return http.DefaultClient.Do(request)
}

func grokWebPlainHTTP() bool {
	return os.Getenv("GROK_WEB_PLAIN_HTTP") == "1"
}

// grokCloudflareChallengeRE matches the Cloudflare interstitial markers
// upstream sniffs for on a refused body.
var grokCloudflareChallengeStrings = []string{"just a moment", "attention required", "cf-mitigated"}

// grokIsCloudflareChallenge reports whether a refused body is the Cloudflare
// interstitial rather than one of Grok's own errors. The distinction decides
// the message the operator sees: a challenge means the cf_clearance in the
// pasted Cookie header no longer matches this machine, not that the SSO
// cookie expired.
func grokIsCloudflareChallenge(body string) bool {
	lower := strings.ToLower(body)
	for _, marker := range grokCloudflareChallengeStrings {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// grokWebTimeoutDefault bounds one turn when the caller has no deadline.
const grokWebTimeoutDefault = 120 * time.Second

// grokRequestTimeout reads the per-kind timeout: video turns run five
// minutes, image turns close to three, chat two.
func grokRequestTimeout(kind string) time.Duration {
	switch kind {
	case "video":
		return 300 * time.Second
	case "image":
		return 170 * time.Second
	default:
		return grokWebTimeoutDefault
	}
}
