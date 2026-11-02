package chat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"

	"9router/proxy/internal/providers"
)

// Provider-level admission control helpers.
//
// The keys used here are deliberately derived rather than threaded through the
// call chain: forwardRequest already receives the provider config and the API
// key, and every OpenAI-compatible lane converges on it, so deriving the
// account and proxy identity at that one point covers all of them without
// changing a signature per lane.
//
// The account key is a digest of the credential, not the credential itself, so
// it can appear in logs and the dashboard's resilience panel without disclosing
// a key. The proxy component is the proxy pool id carried on the config's
// HeaderOverrides by the connection that built it; absent that, the request is
// direct and the semaphore key groups all direct traffic for the provider.

// resilienceAdmit consults the breaker then reserves the account's slot.
//
// The breaker is checked first and the semaphore only entered when it passes:
// queueing behind an account while the provider itself is failing would hold
// requests for a reset timeout no account slot can shorten.
//
// allowed=false means the request must not go out. The caller reports it to the
// client as a 503, which is what the caller would have received from the
// failing provider anyway — the breaker only saves the round trip.
func (h *ChatHandler) resilienceAdmit(ctx context.Context, provider string, cfg *providers.ProviderConfig) (func(), bool) {
	if h.Resilience == nil {
		return func() {}, true
	}
	account := resilienceAccountKey(cfg, h.apiKeyForResilience(cfg))
	proxy := resilienceProxyKey(cfg)
	release, ok := h.Resilience.Reserve(ctx, providers.ResolveAlias(provider), account, proxy)
	if !ok {
		return nil, false
	}
	return release, true
}

// resilienceReport charges a provider-level outcome to the breaker and releases
// the account slot.
//
// Only provider-level statuses count. A 401, 429 or plain 400 says something
// about the account or the request, and charging it to the breaker would trip
// the breaker for every healthy account sharing the provider — which is the
// opposite of what the layer is for.
func (h *ChatHandler) resilienceReport(provider string, cfg *providers.ProviderConfig, status int) {
	if h.Resilience == nil {
		return
	}
	if status == 0 {
		// No response was produced: a dial failure, a reset connection, a
		// context that expired before headers. There is no status to classify,
		// and from the breaker's side the provider did not answer — which is
		// exactly what a 503 means. Counting it is what lets a provider that
		// stopped accepting connections be held back.
		h.Resilience.ReportFailure(providers.ResolveAlias(provider),
			resilienceAccountKey(cfg, h.apiKeyForResilience(cfg)), resilienceProxyKey(cfg), nil, 0)
		return
	}
	if !resilienceIsProviderFailure(status) {
		// Not a provider failure: the breaker is neither charged nor cleared.
		// A 2xx still counts as success, which is what lets a provider recover.
		if status >= 200 && status < 300 {
			h.Resilience.ReportSuccess(providers.ResolveAlias(provider),
				resilienceAccountKey(cfg, h.apiKeyForResilience(cfg)), resilienceProxyKey(cfg), nil)
		}
		return
	}
	h.Resilience.ReportFailure(providers.ResolveAlias(provider),
		resilienceAccountKey(cfg, h.apiKeyForResilience(cfg)), resilienceProxyKey(cfg), nil, status)
}

// resilienceIsProviderFailure reports whether a status counts against the
// breaker. 429 is deliberately excluded: it is per-account rate limiting, and
// counting it would trip the breaker once a few accounts are individually
// throttled, blocking every healthy account with them.
func resilienceIsProviderFailure(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable,
		http.StatusGatewayTimeout, 520, 524:
		return true
	default:
		return false
	}
}

// apiKeyForResilience returns the credential the request will actually send.
// The default key is the fallback for providers that authenticate every caller
// the same way, so it still identifies the account for keying purposes.
func (h *ChatHandler) apiKeyForResilience(cfg *providers.ProviderConfig) string {
	if cfg == nil {
		return ""
	}
	if cfg.DefaultAPIKey != "" {
		return cfg.DefaultAPIKey
	}
	return ""
}

// resilienceAccountKey digests the credential so it can be logged and shown in
// the dashboard without disclosing the key. Two connections to the same
// provider with different credentials get different gates, which is what stops
// one account's concurrency from throttling another's.
func resilienceAccountKey(cfg *providers.ProviderConfig, apiKey string) string {
	if apiKey == "" {
		// No credential to distinguish accounts by: key on the endpoint so at
		// least separate compatible nodes do not share a gate.
		return "host:" + resilienceHost(cfg)
	}
	sum := sha256.Sum256([]byte(apiKey))
	return "key:" + hex.EncodeToString(sum[:8])
}

// resilienceProxyKey returns the proxy component of the semaphore key. The
// connection that built the config records its pool on HeaderOverrides; absent
// that the traffic is direct.
func resilienceProxyKey(cfg *providers.ProviderConfig) string {
	if cfg == nil || cfg.HeaderOverrides == nil {
		return "direct"
	}
	for _, k := range []string{"x-9router-proxy-pool", "X-9Router-Proxy-Pool"} {
		if v, ok := cfg.HeaderOverrides[k]; ok && v != "" {
			return v
		}
	}
	return "direct"
}

// resilienceHost extracts the host from a provider's base URL, for the
// no-credential keying case.
func resilienceHost(cfg *providers.ProviderConfig) string {
	if cfg == nil || cfg.BaseURL == "" {
		return "unknown"
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Host == "" {
		return cfg.BaseURL
	}
	return u.Host
}
