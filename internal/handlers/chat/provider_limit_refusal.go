package chat

// The refusal half of provider limits: what a client is told, and the error type
// the failover loops use to tell a provider's own refusal apart from an upstream
// failure. The enforcement half — state, policy reads, the limiter, the debug
// trail — lives in provider_limits.go.

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
)

// The axes a node-state refusal reports. They share the attempt shape with the
// congestion limits so one debug trail carries both, and they are distinct values
// because what a client must do differs: waiting fixes a busy endpoint, and only an
// operator ends a contract or lifts a spent quota.
const (
	axisContract   = "contract"
	axisTokenQuota = "token_quota"
)

// providerRefusalStatus derives the HTTP status from the axis. Congestion is a 429:
// wait and retry. Node state is a 403: no amount of waiting ends an expired
// contract, so Retry-After would promise something that cannot happen.
func providerRefusalStatus(axis string) int {
	switch axis {
	case axisContract, axisTokenQuota:
		return http.StatusForbidden
	default:
		return http.StatusTooManyRequests
	}
}

// providerRefusalType is the error type a client branches on.
func providerRefusalType(axis string) string {
	switch axis {
	case axisContract:
		return "provider_contract_expired"
	case axisTokenQuota:
		return "provider_token_quota_exhausted"
	default:
		return "rate_limit_exceeded"
	}
}

// providerLimitedBody is the refusal body for a request whose candidates were all
// refused, whether by their own congestion limits or by a node's own state.
//
// It carries every attempt, not just the last: the client's actionable choice is
// between waiting for the provider named first and reconfiguring, and it cannot
// make that choice from a single refusal. `response.error.attempts` is the
// documented shape (docs/provider-limits-plan.md §7).
func providerLimitedBody(attempts []providerLimitAttempt) []byte {
	last := attempts[len(attempts)-1]
	payload := map[string]any{
		"error": map[string]any{
			"message":    fmt.Sprintf("provider %s refused this request (%s)", last.Provider, last.Axis),
			"type":       providerRefusalType(last.Axis),
			"scope":      providerLimitScope,
			"code":       providerRefusalStatus(last.Axis),
			"retryAfter": last.RetryAfter,
			"attempts":   attempts,
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		// The body is built from ints and strings, so this cannot fail in
		// practice; a fixed body keeps a marshalling surprise from turning a
		// refusal into a 500.
		return []byte(`{"error":{"message":"provider refused this request","type":"rate_limit_exceeded","scope":"provider","code":429}}`)
	}
	return encoded
}

// providerLimitError is a refusal that names the attempt that produced it, so the
// failover loops can tell it apart from an upstream 429 without reading its
// message text — which is also why it exists as a type rather than a sentinel:
// the loop needs the attempt, not a boolean.
//
// It embeds the upstream error instead of replacing it, so every writer that
// already answers `errors.As(err, &ue)` keeps reporting the 429 and its body
// unchanged, and only the callers that need the extra fact have to ask for it.
type providerLimitError struct {
	*upstreamError
	attempt providerLimitAttempt
}

// Unwrap exposes the embedded upstream error to errors.As, which is what makes
// the existing write sites work with no changes at all.
func (e *providerLimitError) Unwrap() error { return e.upstreamError }

func (h *ChatHandler) providerLimitError(ctx context.Context, provider string, attempt providerLimitAttempt) error {
	state := providerLimitStateFrom(ctx)
	attempts := state.recordRefusal(attempt)
	return &providerLimitError{
		upstreamError: &upstreamError{
			StatusCode: providerRefusalStatus(attempt.Axis),
			Body:       providerLimitedBody(attempts),
		},
		attempt: attempt,
	}
}

// providerRefusalMessage is the one line the dashboard's node-health row shows.
// The two families need different wording because their remedies differ: a
// congestion refusal clears itself, and a node-state refusal waits for an
// operator.
func providerRefusalMessage(provider string, attempt providerLimitAttempt) string {
	switch attempt.Axis {
	case axisContract:
		return fmt.Sprintf("provider %s contract has ended; an operator must renew it", provider)
	case axisTokenQuota:
		return fmt.Sprintf("provider %s lifetime token quota is spent (%d)", provider, attempt.Limit)
	default:
		return fmt.Sprintf("provider %s refused this request at its %s limit (%d)", provider, attempt.Axis, attempt.Limit)
	}
}

// providerLimitRefusal reports whether err is a refusal produced by a provider's
// own limits, and which attempt it names.
//
// The failover loops need the distinction because a provider refusal is neither an
// account fault nor a bad model: it must not cost the connection a cooldown, and
// it must not end the pass while another candidate is untried.
func providerLimitRefusal(err error) (providerLimitAttempt, bool) {
	var refusal *providerLimitError
	if !errors.As(err, &refusal) || refusal == nil {
		return providerLimitAttempt{}, false
	}
	return refusal.attempt, true
}
