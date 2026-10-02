package proxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/log"
)

// UpstreamError captures a non-200 upstream response.
type UpstreamError struct {
	StatusCode int
	Body       []byte
	// Header is the upstream response's headers. Rate-limit carriers such as
	// Retry-After live only here — a Gemini/Antigravity 429 body repeats the
	// wait in a google.rpc.RetryInfo payload at best — and the error is built
	// where the response is still in hand, so this is the only chance to keep
	// them.
	Header http.Header
}

func (e *UpstreamError) Error() string {
	// Include the upstream body (truncated) so 4xx/5xx failures are diagnosable
	// from the fallback log alone — the body often carries Google/Antigravity's
	// actual rejection reason ("Invalid tool parameters", unknown model, etc.).
	body := strings.TrimSpace(string(e.Body))
	if strings.HasPrefix(body, "<!DOCTYPE html") || strings.HasPrefix(body, "<html") {
		lower := strings.ToLower(body)
		if strings.Contains(lower, "cloudflare") || strings.Contains(lower, "attention required") {
			return fmt.Sprintf("upstream returned %d: Cloudflare WAF challenge (Attention Required!): check User-Agent or network proxy", e.StatusCode)
		}
		if titleStart := strings.Index(lower, "<title>"); titleStart != -1 {
			titleEnd := strings.Index(lower[titleStart:], "</title>")
			if titleEnd != -1 {
				titleText := strings.TrimSpace(body[titleStart+7 : titleStart+titleEnd])
				return fmt.Sprintf("upstream returned %d: HTML page (%s)", e.StatusCode, titleText)
			}
		}
		return fmt.Sprintf("upstream returned %d: HTML error page", e.StatusCode)
	}
	if len(body) > 512 {
		body = body[:512] + "... (truncated)"
	}
	if body != "" {
		return fmt.Sprintf("upstream returned %d: %s", e.StatusCode, body)
	}
	return fmt.Sprintf("upstream returned %d", e.StatusCode)
}

// ErrStrictProxyRequired reports a request that was told never to leave over
// the direct IP and could not honour that. Mirrors upstream's
// "[ProxyFetch] Proxy required but failed (strictProxy=true)" refusal
// (decolua/9router#4333).
var ErrStrictProxyRequired = errors.New("proxy required but failed (strictProxy=true)")

var directProxyClient = &http.Client{
	Transport: constants.DefaultHTTPTransportConfig.NewTransport(),
}

// insecureClientCache memoises one InsecureSkipVerify client per proxy URL,
// mirroring the proxyClients cache in the chat handler: a transport pins a
// connection pool, so a client built per retry would leak one pool per
// intercepted request. Only pools an operator actually assigns land here, so
// the map is as small as the connection table and needs no eviction.
var (
	insecureClientsMu sync.RWMutex
	insecureClients   = make(map[string]*http.Client)
)

// strictTLSRequested reports whether the operator opted out of the insecure
// TLS fallback with STRICT_SSL=true or =1, matching upstream's opt-out. Read
// per request rather than cached at init: upstream reads process.env on every
// call, and a test that flips the flag must not need a new process.
func strictTLSRequested() bool {
	v := strings.TrimSpace(os.Getenv("STRICT_SSL"))
	return v == "true" || v == "1"
}

// InsecureClientFor returns the cached client that reaches proxyURL with
// certificate verification disabled, or "" for a direct one. Callers check
// strictTLSRequested first, exactly as upstream checks STRICT_SSL before
// building its insecure dispatcher.
func InsecureClientFor(proxyURL string) *http.Client {
	insecureClientsMu.RLock()
	client, ok := insecureClients[proxyURL]
	insecureClientsMu.RUnlock()
	if ok {
		return client
	}

	insecureClientsMu.Lock()
	defer insecureClientsMu.Unlock()
	if client, ok = insecureClients[proxyURL]; ok {
		return client
	}
	transport := constants.DefaultHTTPTransportConfig.NewTransport()
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	if proxyURL != "" {
		if u, err := url.Parse(proxyURL); err == nil {
			transport.Proxy = http.ProxyURL(u)
		}
	}
	client = &http.Client{Transport: transport, Timeout: directProxyClient.Timeout}
	insecureClients[proxyURL] = client
	return client
}

func isProxyFailure(err error, resp *http.Response) bool {
	if resp != nil && resp.StatusCode == http.StatusForbidden {
		if resp.Header.Get("X-Proxy-Error") != "" || strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/plain") {
			return true
		}
	}
	if err != nil {
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "proxy") || strings.Contains(errStr, "connect tunnel failed") || strings.Contains(errStr, "blocked-by-allowlist") || strings.Contains(errStr, "forbidden") {
			return true
		}
	}
	return false
}

// strictProxyCtxKey carries the strict-proxy decision down to DoRequest.
type strictProxyCtxKey struct{}

// WithStrictProxy marks ctx so DoRequest refuses to replay a failed proxy
// attempt directly (decolua/9router#4333). intended says whether a proxy was
// configured at all, and it is load-bearing: callers such as the Qoder
// executor set strictProxy to mean "do not replay this request directly if the
// proxy fails" — a replayed COSY signature answers 403 — not "a proxy must
// exist". With nothing configured those callers must keep working direct, so
// the intent test belongs in the flag rather than being assumed at the write
// site.
func WithStrictProxy(ctx context.Context, intended bool) context.Context {
	return context.WithValue(ctx, strictProxyCtxKey{}, intended)
}

// strictOnly reports whether ctx forbids the direct replay.
func strictOnly(ctx context.Context) bool {
	intended, ok := ctx.Value(strictProxyCtxKey{}).(bool)
	return ok && intended
}

// newJSONRequest builds one replayable request. body is a []byte, so
// http.NewRequestWithContext installs a GetBody and no replay has to buffer a
// body the first attempt already consumed.
func newJSONRequest(ctx context.Context, method, url string, headers map[string]string, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req, nil
}

// send performs one attempt through client, defaulting to the shared client.
func send(client *http.Client, req *http.Request) (*http.Response, error) {
	if client == nil {
		client = http.DefaultClient
	}
	return client.Do(req)
}

// replayDirect retries the request once through directProxyClient.
func replayDirect(ctx context.Context, method, url string, headers map[string]string, body []byte) (*http.Response, error) {
	directReq, err := newJSONRequest(ctx, method, url, headers, body)
	if err != nil {
		return nil, err
	}
	return send(directProxyClient, directReq)
}

// replayInsecureTLS retries the request once through the cached client that
// skips certificate verification.
func replayInsecureTLS(ctx context.Context, proxyURL, method, url string, headers map[string]string, body []byte) (*http.Response, error) {
	retryReq, err := newJSONRequest(ctx, method, url, headers, body)
	if err != nil {
		return nil, err
	}
	return send(InsecureClientFor(proxyURL), retryReq)
}

// proxyURLFor reports the proxy a client dials, or "" when it dials directly.
func proxyURLFor(client *http.Client) string {
	if client == nil {
		return ""
	}
	tr, ok := client.Transport.(*http.Transport)
	if !ok || tr.Proxy == nil {
		return ""
	}
	// A fixed Proxy func is an *url.URL under the hood, so asking it about a
	// throwaway URL reveals the destination without touching the network.
	req := &http.Request{URL: &url.URL{Scheme: "https", Host: "proxy-target.invalid"}}
	u, err := tr.Proxy(req)
	if err != nil || u == nil {
		return ""
	}
	return u.String()
}

// isCertificateFailure reports whether err is a TLS chain verification
// failure — the class upstream retries with rejectUnauthorized:false. The
// stdlib wraps the cause in a *url.Error, so errors.As unwraps to the typed
// certificate error. A substring test cannot serve here: isProxyFailure's text
// matching would also classify any unrelated error whose message happens to
// mention a proxy.
func isCertificateFailure(err error) bool {
	if err == nil {
		return false
	}
	var verification *tls.CertificateVerificationError
	if errors.As(err, &verification) {
		return true
	}
	var unknownAuthority x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthority) {
		return true
	}
	var invalid x509.CertificateInvalidError
	if errors.As(err, &invalid) {
		return true
	}
	var hostname x509.HostnameError
	return errors.As(err, &hostname)

}

// directReplayForbidder is implemented by the transport a strict pool is
// handed, which is how the decision travels with a cached client: proxy
// clients are pooled per URL, so two connections can name the same URL and
// disagree about strict, and no caller should have to know which it holds.
type directReplayForbidder interface {
	forbidsDirectReplay() bool
}

// forbidDirectReplayTransport is the wrapper ForbidDirectReplay installs.
type forbidDirectReplayTransport struct{ base http.RoundTripper }

func (t forbidDirectReplayTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.base.RoundTrip(req)
}

func (forbidDirectReplayTransport) forbidsDirectReplay() bool { return true }

// ForbidDirectReplay returns a client whose requests may never be replayed
// from the operator's real IP after a proxy refusal (decolua/9router#4333).
func ForbidDirectReplay(client *http.Client) *http.Client {
	clone := *client
	clone.Transport = forbidDirectReplayTransport{base: client.Transport}
	return &clone
}

// forbidsDirectReplay reports whether a pooled client asked for the marker.
func forbidsDirectReplay(client *http.Client) bool {
	if client == nil {
		return false
	}
	marker, ok := client.Transport.(directReplayForbidder)
	return ok && marker.forbidsDirectReplay()
}

// ForbidsDirectReplay reports whether client was marked to refuse the direct
// replay, i.e. whether it is the client a strict pool handed out.
func ForbidsDirectReplay(client *http.Client) bool {
	return forbidsDirectReplay(client)
}

// DoRequest sends an HTTP POST to url with body and auth, returns the raw response.
// Caller must close resp.Body.
//
// A proxy refusal is replayed through the direct client so one dead proxy does
// not take the provider down with it — unless the proxy is one the operator
// assigned, or ctx carries WithStrictProxy. Both turn that replay into an error
// instead of publishing the operator's egress IP (decolua/9router#4333). A
// certificate verification failure gets its own single replay with verification
// disabled, for the corporate proxies and antivirus middleboxes that re-sign
// TLS, unless STRICT_SSL forbids it.
func DoRequest(ctx context.Context, client *http.Client, method, url string, headers map[string]string, body []byte) (*http.Response, error) {
	return retryTransientUpstream(ctx, func() (*http.Response, error) {
		return doRequestOnce(ctx, client, method, url, headers, body)
	})
}

// doRequestOnce performs a single attempt.
func doRequestOnce(ctx context.Context, client *http.Client, method, url string, headers map[string]string, body []byte) (*http.Response, error) {
	req, err := newJSONRequest(ctx, method, url, headers, body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := send(client, req)

	switch {
	case isCertificateFailure(err):
		if strictTLSRequested() {
			break
		}
		log.Warn("proxy", "TLS verification failed, retrying without certificate verification", "url", url, "error", err)
		if resp, err = replayInsecureTLS(ctx, proxyURLFor(client), method, url, headers, body); err != nil {
			return nil, fmt.Errorf("upstream request after insecure TLS retry: %w", err)
		}
	case isProxyFailure(err, resp):
		if resp != nil {
			resp.Body.Close()
		}
		// A strict connection refuses on the context; a strict pool refuses
		// through the cached client it handed out, so every caller of the
		// shared cache is covered without threading a flag through each of
		// them. A proxy pool the operator assigned is refused on the same
		// grounds: the direct replay below would answer the request from the
		// host's real IP while the dashboard still shows it as proxied.
		if strictOnly(req.Context()) || forbidsDirectReplay(client) || proxiesViaAssignment(client) {
			return nil, fmt.Errorf("assigned proxy failed, refusing to route direct (%w): %w", err, ErrStrictProxyRequired)
		}
		log.Warn("proxy", "request through the proxy failed, retrying direct", "url", url, "error", err)
		if resp, err = replayDirect(ctx, method, url, headers, body); err != nil {
			return nil, fmt.Errorf("upstream request direct after proxy failure: %w", err)
		}
	}

	if err != nil {
		return nil, fmt.Errorf("upstream request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		errBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("upstream returned %d and body read failed: %w", resp.StatusCode, readErr)
		}
		return nil, &UpstreamError{StatusCode: resp.StatusCode, Body: errBody, Header: resp.Header}
	}
	return resp, nil
}