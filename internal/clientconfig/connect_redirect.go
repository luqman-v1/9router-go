package clientconfig

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// connectTimeout bounds the whole redirect chain, not one hop: the client's
// Timeout covers every request it makes on the caller's behalf.
const connectTimeout = 15 * time.Second

// maxConnectRedirects is the hop ceiling of §4.4. Five is already unusual for
// an API endpoint, and every hop is one more chance for the origin to change.
const maxConnectRedirects = 5

// origin is the trust boundary for a connect request: scheme, canonical
// hostname, and effective port. A different port is a different origin, even on
// the same address — that is the gap net/http leaves open (§4.1).
//
// It is exported because ConnectRedirectPolicy takes it: a policy that cannot
// be built by the caller that owns the endpoint is a policy nobody can install.
type Origin struct {
	scheme string
	host   string
	port   string
}

// OriginOf derives the origin of a URL the same way the canonical form is
// built, so a validated endpoint and a redirect target are compared on equal
// terms.
func OriginOf(u *url.URL) Origin {
	if u == nil {
		return Origin{}
	}
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	if port == "" {
		switch scheme {
		case "https":
			port = "443"
		case "http":
			port = "80"
		}
	}
	return Origin{
		scheme: scheme,
		host:   normalizeConnectHost(u.Hostname()),
		port:   port,
	}
}

// ConnectRedirectPolicy is the CheckRedirect for every client in the connect
// flow. allowed is the origin validated at §3.8.
//
// Both rules are invariants, not heuristics:
//   - (a) has no exception, not even for a loopback destination. Once a chain
//     has been on HTTPS, every later hop is HTTPS (§4.3).
//   - (b) reads the credential actually on the request, not a guess from session
//     state. It looks at the previous request as well as the outgoing one:
//     net/http strips Cookie and Authorization before calling CheckRedirect when
//     the hostname changes, so checking only req would let a hostname hop
//     through with an empty header. The previous request still carries it, and
//     that is the fact the decision turns on.
//
// The credential check covers the request BODY as well as the headers. The
// connect flow authenticates with a JSON body ({"password": …}), never with a
// header, and net/http replays a 307/308 body verbatim to the new origin
// (client.go: includeBody). A header-only check therefore let the dashboard
// password — the one credential the flow actually sends — travel to a host the
// user never named.
func ConnectRedirectPolicy(allowed Origin, assertPublic AssertPublicFunc) func(req *http.Request, via []*http.Request) error {
	if assertPublic == nil {
		assertPublic = DefaultAssertPublic
	}
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxConnectRedirects {
			return fmt.Errorf("%w: stopped after %d redirects", ErrTooManyRedirects, maxConnectRedirects)
		}
		prevReq := via[len(via)-1]
		prev := prevReq.URL
		next := req.URL

		// (a) Never downgrade.
		if strings.EqualFold(prev.Scheme, "https") && !strings.EqualFold(next.Scheme, "https") {
			return fmt.Errorf("%w: %s -> %s", ErrRedirectDowngrade, redactURL(prev), redactURL(next))
		}
		// (a2) A redirect target that carries userinfo is refused outright.
		// The first URL is validated against userinfo (§3.4); a Location header
		// is server-controlled, so it is exactly the place a "user:pass@" would
		// be injected to make the client send Basic credentials to a host the
		// user never named. The sentinel carries no URL, so nothing is echoed.
		if next.User != nil {
			return fmt.Errorf("%w: redirect target", ErrUserinfoNotAllowed)
		}
		// (b) Never change origin while carrying a credential — in a header or
		// in the body.
		if OriginOf(next) != allowed && (reqHasCredential(req) || reqHasCredential(prevReq) || reqHasBody(req, prevReq)) {
			return fmt.Errorf("%w: %s -> %s", ErrRedirectCrossOrigin, redactURL(prev), redactURL(next))
		}
		// (c) Every hop is validated the way the first URL was. Step 8 is the
		// only layer that resolves DNS and checks the resolved address
		// (§3.7); without it a public host can redirect to 169.254.169.254 or
		// an RFC1918 address and the guard never sees it.
		if OriginOf(next) != allowed {
			if err := assertPublic(next.String()); err != nil {
				return fmt.Errorf("%w: redirect target %s: %w", ErrBlockedHost, redactURL(next), err)
			}
		}
		return nil
	}
}

// reqHasBody reports whether the request carries a body that could hold a
// credential.
//
// It is deliberately conservative: any non-empty body counts. The connect flow
// sends exactly one body — the login JSON — and the only requests it makes
// without one are GETs that carry nothing secret. A body that might be a
// credential is treated as one, because the cost of a false positive is one
// refused redirect, and the cost of a false negative is the password.
func reqHasBody(reqs ...*http.Request) bool {
	for _, r := range reqs {
		if r == nil {
			continue
		}
		if r.Body != nil && r.Body != http.NoBody {
			return true
		}
		if r.GetBody != nil && r.ContentLength != 0 {
			return true
		}
	}
	return false
}

// redactURL renders a URL without its userinfo, for an error message. The
// password must never be printed (§3.4); the rest of the URL is what tells the
// user where the server sent them.
func redactURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	clean := *u
	clean.User = nil
	return clean.String()
}

// DoConnect sends one request and returns an error whose URL is redacted.
//
// It is the single send path for the connect flow, and it exists because of a
// stdlib leak: when a CheckRedirect policy refuses a hop, net/http writes the
// raw Location header into the *url.Error it returns (client.go:
// `ue.(*url.Error).URL = loc`). That value is server-controlled and may carry a
// credential, and it lands in the CLI's stderr and in every log that captured
// it. The error text this package produces is already clean; this closes the
// stdlib half.
//
// One helper rather than a redaction at each call site: a guard that every
// caller must remember is a guard that one caller forgets.
func DoConnect(client *http.Client, req *http.Request) (*http.Response, error) {
	if client == nil {
		return nil, fmt.Errorf("%w: nil client", ErrInvalidURL)
	}
	resp, err := client.Do(req)
	return resp, RedactConnectError(err)
}

// RedactConnectError removes userinfo from the URL an error carries.
//
// It returns the same error value with a redacted URL, so errors.Is still
// works on the sentinel.
func RedactConnectError(err error) error {
	if err == nil {
		return nil
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.URL != "" {
		if parsed, parseErr := url.Parse(urlErr.URL); parseErr == nil && parsed.User != nil {
			redacted := *urlErr
			redacted.URL = redactURL(parsed)
			return &redacted
		}
	}
	return err
}

// reqHasCredential reports whether the request that is about to be sent carries
// a cookie or a bearer token. net/http strips both only when the hostname
// changes; a port change and a scheme downgrade leave them in place, which is
// exactly what rule (b) has to catch.
func reqHasCredential(req *http.Request) bool {
	if req == nil {
		return false
	}
	return req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != ""
}

// NewConnectClient builds the one HTTP client the connect flow may use.
//
// Timeout and the redirect policy are mandatory. Jar is deliberately nil: a
// cookie jar replays cookies on its own, across requests this code never
// inspected, and the whole point of §4.3 is that nothing is sent anywhere the
// caller did not decide to send it. Without a jar the cookie is attached per
// request, so the code attaching it is the only code that knows the origin.
//
// assertPublic is the step-8 seam. nil means the production SSRF guard. It is
// applied to every redirect hop as well as the first URL, because step 8 is
// the only layer that resolves DNS (§3.7).
func NewConnectClient(rt http.RoundTripper, endpoint ConnectEndpoint, assertPublic AssertPublicFunc) *http.Client {
	if rt == nil {
		rt = http.DefaultTransport
	}
	return &http.Client{
		Timeout:       connectTimeout,
		Transport:     rt,
		CheckRedirect: ConnectRedirectPolicy(OriginOf(endpoint.URL), assertPublic),
		Jar:           nil,
	}
}
