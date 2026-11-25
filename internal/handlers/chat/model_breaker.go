package chat

import (
	json "encoding/json/v2"
	"net/http"
	"strconv"
	"time"

	"9router/proxy/internal/modelbreaker"
)

// Per-model admission control, wired at the one point every OpenAI-compatible
// lane converges on.
//
// This layer answers a question the two breakers already in the tree do not.
// internal/resilience/open-sse is per-provider: when one model in a healthy
// provider starts failing, opening the whole provider takes every sibling
// offline with it. internal/modelhealth probes models on a schedule and disables
// rows; it does not watch live traffic, so a model that began failing thirty
// seconds ago keeps receiving requests until the next pass.
//
// It reads its identity from forwardRequestParams, which already carries both
// Provider and Model at the call site — the same pair a custom provider node
// resolves to through ModelInfo.Provider, and the same pair a registered
// provider's connection produces. So one call site covers both, and a custom
// OpenAI/Anthropic-compatible node is not a special case: it is a node id in the
// provider slot, exactly as it is everywhere else in this handler.
//
// The gate is deliberately before the provider-level breaker's semaphore: a
// model that is out of rotation must not queue behind an account slot it will
// never use.

// modelBreakerOptions are the per-model policy defaults. The base cooldown and
// the escalation match Pro pro.26's per-model numbers (60s doubling, capped at
// 10 minutes) and the disable threshold is the operator's chosen three cycles.
var modelBreakerOptions = modelbreaker.Options{}

// modelBreakerAllowed reports whether this provider/model pair may take another
// request. It is false only while a cooldown is running or after the model has
// been disabled, and it is what lets the caller fail over instead of dialling a
// model that is known to be failing.
func modelBreakerAllowed(provider, model string) bool {
	if provider == "" || model == "" {
		// A caller that cannot name its target cannot be gated: charging or
		// refusing an unknown pair would put the request path at the mercy of
		// a caller that forgot to populate the fields.
		return true
	}
	return modelbreaker.BreakerFor(provider, model, modelBreakerOptions).Allow()
}

// modelBreakerRetryAfter reports how long until the pair is probed again, for
// the Retry-After hint and the log line. Zero means "not the model breaker",
// which is the case the caller must not mistake for a cooldown.
func modelBreakerRetryAfter(provider, model string) (time.Duration, bool) {
	if provider == "" || model == "" {
		return 0, false
	}
	b := modelbreaker.BreakerFor(provider, model, modelBreakerOptions)
	if b.State() != modelbreaker.StateCooling {
		return 0, false
	}
	return b.RetryAfter(), true
}

// modelBreakerSuccess clears the pair. A model that answered once is serving.
func modelBreakerSuccess(provider, model string) {
	if provider == "" || model == "" {
		return
	}
	modelbreaker.BreakerFor(provider, model, modelBreakerOptions).RecordSuccess()
}

// modelBreakerFailure charges one outcome.
//
// The status is classified, not pattern-matched, and only a model-scoped
// failure counts: internal/errclass's ScopeModel means the model itself is gone
// or the prompt does not fit it. A provider-scoped 5xx or transport error is
// internal/resilience/open-sse's to charge — counting it here would put every
// model of a briefly-down provider into cooldown and then disable all of them
// after three cycles, which is the failure this layer exists to prevent. The
// same reasoning excludes an account-scoped 429 or an auth error, which belong
// to the connection-rotation layer.
func modelBreakerFailure(provider, model string, status int, body []byte) {
	if provider == "" || model == "" {
		return
	}
	if !modelbreaker.CountsTowardBreaker(modelbreaker.Classify(status, body)) {
		return
	}
	modelbreaker.BreakerFor(provider, model, modelBreakerOptions).RecordFailure()
}

// modelBreakerUnavailable is the error a caller returns when the model breaker
// refuses. It is a 503 with Retry-After, because that is what the client would
// have received from the failing model anyway — the breaker only saves the
// round trip and the quota it would have burned.
//
// Local marks it: the credential was never dialled, so the account-rotation
// layer must not cool a healthy connection down over a verdict about a model.
// breakerBody is marshalled once at init from a typed struct, so the envelope
// on the wire is guaranteed parseable — the previous hand-concatenated literal
// emitted `""type"`.
var breakerBody = func() []byte {
	b, err := json.Marshal(struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}{
		Error: struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		}{Message: "model is cooling down after repeated failures", Type: "server_error", Code: "model_cooling_down"},
	})
	if err != nil {
		// A struct of three strings cannot fail to marshal; a panic here would
		// be a programming error, and the request path must not crash on it.
		return []byte(`{"error":{"message":"model is cooling down after repeated failures","type":"server_error","code":"model_cooling_down"}}`)
	}
	return b
}()

// modelBreakerUnavailable builds the refusal. Kept as a function because
// retryAfter varies per call.
func modelBreakerUnavailable(retryAfter time.Duration) *upstreamError {
	ra := ""
	if retryAfter > 0 {
		ra = strconv.Itoa(int(retryAfter.Seconds()))
	}
	return &upstreamError{
		StatusCode: http.StatusServiceUnavailable,
		// One literal, not two concatenated halves: a split after `"message":…,`
		// and before `"type":…` produced `""type"` — valid-looking bytes that
		// json.Unmarshal rejects, so a client parsing the 503 envelope saw a
		// decode error instead of the reason. `json.Marshal` of a struct is
		// what keeps that class of bug out of the wire format.
		Body:   breakerBody,
		Header: http.Header{"Retry-After": []string{ra}},
		Local:  true,
	}
}
