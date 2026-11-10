package errclass

import (
	"bytes"
	"encoding/json"
	"strings"
)

// classify.go decides what a failure means, not how long to back off. The
// verdict is a Classification (Kind + Scope), left for the caller to act on:
// a context-window overflow wants the next model with a bigger window, a
// content-filter rejection wants a provider with a different policy, and a
// dead credential wants the account out of rotation. A cooldown duration
// belongs to the rotation layer, which keeps its own classifier
// (providers.ClassifyError); this package deliberately stops at "what is it".

// statusFromCodeAndStatus resolves a usable HTTP status when the frame carries
// no numeric code but a Google-style status string. Different providers
// disagree on the carrier — Gemini sends "status":"RESOURCE_EXHAUSTED", the
// gRPC-derived envelopes send the same strings under a Status field — and a
// wrapper 5xx frequently hides a 429 that the code would otherwise miss.
func statusFromCodeAndStatus(code int, status string) int {
	if code != 0 {
		return code
	}
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "RESOURCE_EXHAUSTED":
		return 429
	case "UNAVAILABLE":
		return 503
	case "DEADLINE_EXCEEDED":
		return 504
	case "PERMISSION_DENIED", "UNAUTHENTICATED":
		return 403
	case "NOT_FOUND":
		return 404
	case "INVALID_ARGUMENT", "FAILED_PRECONDITION":
		return 400
	case "INTERNAL":
		return 500
	default:
		return 0
	}
}

// classifyType runs first because it is the most precise signal: the provider
// names its own failure ("overloaded_error", "rate_limit_error"), so no body
// prose has to be interpreted and no status has to be guessed. The third
// result is the status to fall back to when the frame carried none — an
// overloaded_error reads as 529, a plain api_error as 500.
func classifyType(lowerType string) (Kind, Scope, int, bool) {
	switch lowerType {
	case "overloaded_error":
		return KindUpstream, ScopeProvider, 529, true
	case "rate_limit_error", "rate_limit_exceeded", "throttling_error", "tokens", "requests", "insufficient_quota":
		return KindRateLimit, ScopeAccount, 429, true
	case "authentication_error", "permission_error", "invalid_api_key":
		return KindAuth, ScopeAccount, 0, true
	case "billing_error":
		return KindQuotaExhausted, ScopeAccount, 0, true
	case "not_found_error", "model_not_found":
		return KindModelUnavailable, ScopeModel, 0, true
	case "api_error", "internal_server_error", "server_error", "service_unavailable_error":
		return KindUpstream, ScopeProvider, 500, true
	case "timeout_error":
		return KindTimeout, ScopeProvider, 0, true
	default:
		return "", "", 0, false
	}
}

// classifyMessage runs second, on the provider's own prose. The phrase checks
// are ordered by how often they are reliably decisive: capacity reads are
// deliberately scoped to a small vocabulary, and the model-gone check only
// fires when both halves ("model" + not found/unavailable) are present so a
// stray "model" in an unrelated message cannot misfire.
func classifyMessage(lowerMsg string) (Kind, Scope, bool) {
	switch {
	case LooksLikeContextWindow(lowerMsg):
		return KindContextWindow, ScopeModel, true
	case LooksLikeContentFilter(lowerMsg):
		return KindContentFilter, ScopeRequest, true
	case LooksLikeAuthError(lowerMsg):
		// A rejected credential, regardless of the status it arrived with: a
		// provider misfiring a 429 or a 502 can still reject the key in prose,
		// and only a body verdict sees that. Without this the message-only
		// credential failure (no error type, no useful status) falls through to
		// the status map and lands in the upstream/retry bucket, rotating a dead
		// key back in instead of retiring it.
		return KindAuth, ScopeAccount, true
	case containsAny(lowerMsg, []string{
		"rate limit", "rate_limit", "tokens per min", "too many requests",
		"resource exhausted", "quota exceeded",
	}):
		return KindRateLimit, ScopeAccount, true
	case containsAny(lowerMsg, []string{
		"at capacity", "overloaded", "server_is_overloaded", "model_at_capacity",
	}):
		// Capacity, not credentials: rotate attempts without a heavy penalty.
		return KindUpstream, ScopeProvider, true
	case strings.Contains(lowerMsg, "model") && containsAny(lowerMsg, []string{
		"not supported", "not available", "not found", "does not exist",
	}):
		return KindModelUnavailable, ScopeModel, true
	default:
		return "", "", false
	}
}

// classifyStatus is the numeric fallback for a frame whose type and message
// said nothing usable. The code maps follow the semantics the statuses have
// across providers: a 402 is a spent balance, a 404 names a missing model,
// and any other 4xx is about the request, not the credential.
func classifyStatus(code int) (Kind, Scope) {
	switch {
	case code == 429:
		return KindRateLimit, ScopeAccount
	case code == 401, code == 403:
		return KindAuth, ScopeAccount
	case code == 402:
		return KindQuotaExhausted, ScopeAccount
	case code == 404:
		return KindModelUnavailable, ScopeModel
	case code == 408, code == 504:
		return KindTimeout, ScopeProvider
	case code >= 400 && code < 500:
		return KindBadRequest, ScopeRequest
	default:
		return KindUpstream, ScopeProvider
	}
}

// ClassifyDetail maps a provider error type/code/status/message tuple to a
// Classification the failover policy can act on. It is the deterministic core
// of the package: the same tuple always lands in the same bucket.
//
// Precedence is type, then message, then numeric status — the most precise
// and least wording-dependent carrier wins, so a provider that names its own
// error type does not have its prose interpreted and a provider that only
// sends a code still gets a verdict.
func ClassifyDetail(errType string, code int, status, message string) Classification {
	c := Classification{Message: message}
	if c.Message == "" {
		c.Message = errType
	}
	c.StatusCode = statusFromCodeAndStatus(code, status)

	if kind, scope, typeStatus, ok := classifyType(strings.ToLower(errType)); ok {
		c.Kind, c.Scope = kind, scope
		if c.StatusCode == 0 {
			c.StatusCode = typeStatus
		}
		return c
	}
	if kind, scope, ok := classifyMessage(strings.ToLower(message)); ok {
		c.Kind, c.Scope = kind, scope
		return c
	}
	c.Kind, c.Scope = classifyStatus(c.StatusCode)
	return c
}

// streamErrorEnvelope matches the error frames providers emit inside an HTTP
// 200 SSE stream:
//
//	OpenAI:    {"error":{"message":"...","type":"...","code":"..."}}
//	Anthropic: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}
//	Gemini:    {"error":{"code":429,"message":"...","status":"RESOURCE_EXHAUSTED"}}
//	Generic:   {"error":"string message"}
type streamErrorEnvelope struct {
	Type  string          `json:"type"`
	Error json.RawMessage `json:"error"`
}

type streamErrorDetail struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Code    any    `json:"code"`
	Status  string `json:"status"`
}

var errorKey = []byte(`"error"`)

// StreamErrorFrame reports whether an SSE data payload is an in-band error
// frame and, if so, returns its Classification. It is the streaming twin of
// ClassifyDetail: a provider that reports overload or a rate limit inside a
// 200 looks like a clean, empty completion unless the frame is decoded and
// its verdict is acted on — the stream then records as a success, the account
// is never cooled down, and the client receives a well-formed but truncated
// answer.
//
// A nil error field, an event type other than "error" (a legitimate event that
// merely carries an "error" key), "[DONE]" and plain content lines all report
// not-found.
func StreamErrorFrame(payload []byte) (Classification, bool) {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) < 8 || trimmed[0] != '{' {
		return Classification{}, false
	}
	// Cheap pre-check so the vast majority of frames skip the decode.
	if !bytes.Contains(trimmed, errorKey) {
		return Classification{}, false
	}
	var env streamErrorEnvelope
	if json.Unmarshal(trimmed, &env) != nil {
		return Classification{}, false
	}
	errRaw := bytes.TrimSpace(env.Error)
	if len(errRaw) == 0 || string(errRaw) == "null" {
		return Classification{}, false
	}
	if env.Type != "" && env.Type != "error" {
		return Classification{}, false
	}

	var detail streamErrorDetail
	switch errRaw[0] {
	case '{':
		if json.Unmarshal(errRaw, &detail) != nil {
			return Classification{}, false
		}
	case '"':
		var msg string
		if json.Unmarshal(errRaw, &msg) != nil || msg == "" {
			return Classification{}, false
		}
		detail.Message = msg
	default:
		return Classification{}, false
	}
	if detail.Message == "" && detail.Type == "" {
		return Classification{}, false
	}
	return ClassifyDetail(detail.Type, codeToInt(detail.Code), detail.Status, detail.Message), true
}
