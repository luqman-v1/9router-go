package dashboard

import (
	"net/http"
	"net/url"
	"strings"

	"9router/proxy/internal/constants"
)

// probeRelayTarget is the upstream a relay pool fronts: the origin to dial and
// the path the relay should forward to.
type probeRelayTarget struct {
	origin string // scheme://host of the provider, e.g. https://api.openai.com
	path   string // provider path, e.g. /v1/models
}

// splitRelayTarget separates a provider URL the way the relay expects: the relay
// gets the origin in x-relay-target and the path in x-relay-path, and is itself
// dialed at its own host.
func splitRelayTarget(rawURL string) (probeRelayTarget, bool) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" {
		return probeRelayTarget{}, false
	}
	path := parsed.Path
	if path == "" {
		path = "/"
	}
	if parsed.RawQuery != "" {
		path += "?" + parsed.RawQuery
	}
	return probeRelayTarget{origin: parsed.Scheme + "://" + parsed.Host, path: path}, true
}

// newProbeRelayClient builds the client a dashboard probe uses to reach an
// upstream through an edge relay pool (vercel/cloudflare/deno).
//
// probeHTTPClient used to return a nil client for those pools, so the probe ran
// straight at the provider from the host's own IP. That misreports the setup
// twice over: it can pass while production traffic through the relay fails, and
// it can fail while the relay path is healthy — and it publishes the operator's
// egress IP on the very request they ran to confirm a proxy was in place.
func newProbeRelayClient(relayURL, upstreamURL string) *http.Client {
	relay, err := url.Parse(strings.TrimSpace(relayURL))
	if err != nil || relay.Host == "" {
		return nil
	}
	target, ok := splitRelayTarget(upstreamURL)
	if !ok {
		return nil
	}
	return &http.Client{
		Timeout: connectionProbeTimeout,
		Transport: &probeRelayRoundTripper{
			// Its own transport with Proxy unset: the relay host is dialed
			// directly, and an ambient HTTP_PROXY must not intercept it.
			base:   constants.DefaultHTTPTransportConfig.NewTransport(),
			relay:  relay,
			target: target,
		},
	}
}

// probeRelayRoundTripper redirects each outbound probe at the relay host and
// names the real destination in headers, which is exactly what the chat
// pipeline does for a relayed connection.
type probeRelayRoundTripper struct {
	base   http.RoundTripper
	relay  *url.URL
	target probeRelayTarget
}

func (t *probeRelayRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}

	relayed := req.Clone(req.Context())
	relayed.URL.Scheme = t.relay.Scheme
	relayed.URL.Host = t.relay.Host
	// The relay serves many upstreams; the provider path belongs in the header,
	// not in the URL the relay is asked for.
	relayed.URL.Path = "/"
	relayed.URL.RawQuery = ""
	relayed.Host = t.relay.Host
	relayed.RequestURI = ""
	relayed.Header.Set("x-relay-target", t.target.origin)
	relayed.Header.Set("x-relay-path", t.target.path)
	return base.RoundTrip(relayed)
}
