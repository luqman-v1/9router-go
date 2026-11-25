package chat

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/middleware"
	"9router/proxy/internal/proxy"
)

// admitPerModel enforces the per-model cap for custom providers. It returns nil
// for every other provider (the cap exists for endpoints the operator owns, not
// for registry providers whose own quota handling already applies), and for a
// request the limiter admits.
//
// The settings read goes through Repo.PerModelRateLimit, which answers from the
// settings cache instead of a point query: this runs once per candidate
// connection on the dispatch path, and a point query there measured 23.7 µs
// against 0.3 µs for the limiter itself.
func (h *ChatHandler) admitPerModel(ctx context.Context, provider, model string) error {
	if h.Repo == nil || !isCustomCompatibleProvider(provider) {
		return nil
	}
	// A probe (POST /api/models/test, the auto-ping) reports what the model
	// does right now; answering "capped" would report the limiter instead of
	// the model, and charging the ping into the client budget would invert
	// enforcement at low limits — the reviewer's non-blocking finding.
	if handlerutil.IsProbeContext(ctx) {
		return nil
	}
	limit := resolvePerModelRateLimit(h.Repo.PerModelRateLimit(), provider, model)
	if !limit.Enabled {
		return nil
	}

	apiKey := ""
	if k := middleware.AuthenticatedApiKeyFromContext(ctx); k != nil {
		apiKey = k.Key
	}
	scope := perModelScope(handlerutil.GetSessionID(ctx), apiKey)

	allowed, retryAfter := perModelLimiter.allow(provider, model, scope, limit)
	if allowed {
		return nil
	}
	message := fmt.Sprintf("per-model rate limit reached for %s/%s", provider, model)
	// Retry-After in whole seconds, rounded up: a client that retries exactly
	// at the boundary has not yet cleared the window.
	seconds := int(retryAfter.Seconds())
	if retryAfter > 0 && retryAfter%time.Second > 0 {
		seconds++
	}
	if seconds < 1 {
		seconds = 1
	}
	header := http.Header{}
	header.Set("Retry-After", strconv.Itoa(seconds))
	return &proxy.UpstreamError{
		StatusCode: http.StatusTooManyRequests,
		Local:      true,
		Header:     header,
		Body: []byte(fmt.Sprintf(
			`{"error":{"message":%q,"type":"rate_limit_error","code":429}}`, message)),
	}
}

// isCustomCompatibleProvider reports whether a provider id is one of the custom
// node shapes. The per-model cap applies only here: a registry provider's quota
// is enforced upstream and by the connection-level limiter, and adding a second
// limiter in front of it would cap traffic the operator never asked to cap.
func isCustomCompatibleProvider(provider string) bool {
	return strings.HasPrefix(provider, "openai-compatible-") ||
		strings.HasPrefix(provider, "anthropic-compatible-")
}
