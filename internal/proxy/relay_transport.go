package proxy

import (
	"net/http"
	"net/url"
	"strings"
)

// Edge-relay transport.
//
// A vercel/cloudflare/deno/netlify pool is not a CONNECT proxy: it is an
// ordinary HTTPS endpoint that forwards to whatever x-relay-target and
// x-relay-path name. Upstream expresses this at fetch time — proxyAwareFetch
// takes proxyOptions.vercelRelayUrl, rewrites the *dial* and leaves the
// requested URL untouched (open-sse/utils/proxyFetch.js).
//
// This port expressed it as a per-provider convention instead: the handler
// rewrote the provider's BaseURL to the relay host and stamped the two headers
// into StaticHeaders, and every executor that built its own requests had to
// re-apply the contract by hand. Four did (qoder, opencode-zen/go,
// minimax-code, freebuff) and every other one silently leaked: trae appended
// its session path to the relay host, zed and windsurf dialled the provider
// directly, and the embeddings lane never consulted the pool at all.
//
// A RoundTripper removes the convention. The request is built exactly as it
// would be for a direct call — real provider URL, real path, real query — and
// the transport moves the dial to the relay while naming that real URL in the
// headers. Every provider gets relay support by construction, including ones
// that do not exist yet, and an executor that signs a URL (Qoder's COSY) signs
// the URL it actually meant.

// RelayTransport redirects every request it sees at an edge relay, naming the
// request's own URL as the destination.
//
// A request that already carries x-relay-target is passed through untouched:
// that is an executor which routed itself, and re-stamping it would overwrite
// the lane it deliberately chose.
func RelayTransport(relayURL string, base http.RoundTripper) http.RoundTripper {
	relay, err := url.Parse(strings.TrimSpace(relayURL))
	if err != nil || relay.Host == "" {
		return base
	}
	if base == nil {
		base = http.DefaultTransport
	}
	return &relayTransport{base: base, relay: relay}
}

type relayTransport struct {
	base  http.RoundTripper
	relay *url.URL
}

func (t *relayTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	relayed := req.Clone(req.Context())
	// An executor that already named a destination keeps it: opencode-zen picks a
	// lane path per model, and re-stamping would overwrite that choice. The dial
	// still moves — naming a target is not dialling the relay — which is the bug
	// the first cut of this transport had.
	if relayed.Header.Get("x-relay-target") == "" {
		// The destination is the URL the caller built, in full: a provider that
		// appends its own lane path, or a signed request whose path is part of the
		// signature, keeps it.
		relayed.Header.Set("x-relay-target", req.URL.Scheme+"://"+req.URL.Host)
		relayed.Header.Set("x-relay-path", requestPath(req.URL))
	}

	// The dial goes to the relay's own path, which for a Netlify deployment is
	// the function route rather than "/".
	relayed.URL.Scheme = t.relay.Scheme
	relayed.URL.Host = t.relay.Host
	relayed.URL.Path = t.relay.Path
	relayed.URL.RawPath = t.relay.RawPath
	relayed.URL.RawQuery = t.relay.RawQuery
	relayed.Host = t.relay.Host
	relayed.RequestURI = ""
	return t.base.RoundTrip(relayed)
}

// requestPath is the path plus query a relay should forward to. A URL with no
// path still needs one, or the relay forwards to an empty target.
func requestPath(u *url.URL) string {
	path := u.Path
	if path == "" {
		path = "/"
	}
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	return path
}

// RelayEndpoint reports the destination an edge relay is configured to front,
// from the pair the transport stamps, or "" when the request is not relayed.
// The dashboard probe and the egress label read it, so both agree with the wire.
func RelayEndpoint(headers map[string]string) string {
	target := strings.TrimSpace(headers["x-relay-target"])
	if target == "" {
		return ""
	}
	path := strings.TrimSpace(headers["x-relay-path"])
	if path == "" {
		return target
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return strings.TrimRight(target, "/") + path
}
