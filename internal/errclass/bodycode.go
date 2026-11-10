package errclass

import (
	"encoding/json"
	"strings"
	"time"
)

// bodycode.go recovers the HTTP-status signal from a raw upstream body and
// clamps upstream-suggested waits. Both are the pieces of classification that
// work on bytes rather than on the already-decoded tuple: a gateway that wraps
// a provider 429 in a 5xx hides the truthful code inside the JSON, and a
// hostile Retry-After would otherwise park an account far past the condition
// that made the provider suggest it.

// BodyErrorCode extracts a numeric status from common JSON error envelopes:
//
//	{"error":{"code":429}}, {"error":{"code":"429"}}, {"code":429},
//	{"error":{"status":"RESOURCE_EXHAUSTED"}} (Gemini)
//
// It returns 0 when no usable numeric code is present. Gateways and reverse
// proxies frequently wrap a provider 429 in a 5xx; the embedded code is the
// more truthful signal, and reading it lets a caller discover a rate limit
// that a blind status check would call a plain 5xx.
func BodyErrorCode(body []byte) int {
	if len(body) == 0 || body[0] != '{' {
		return 0
	}
	var env struct {
		Code  any `json:"code"`
		Error *struct {
			Code   any    `json:"code"`
			Status string `json:"status"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &env) != nil {
		return 0
	}
	if env.Error != nil {
		if c := codeToInt(env.Error.Code); c > 0 {
			return c
		}
		if c := statusToCode(env.Error.Status); c > 0 {
			return c
		}
	}
	return codeToInt(env.Code)
}

// codeToInt reads a JSON code value that providers send as either a number or
// a 3-digit numeric string. Anything else — "insufficient_quota",
// "upstream_error", a four-digit code, a negative number — is not an HTTP
// status and yields 0.
func codeToInt(v any) int {
	switch n := v.(type) {
	case float64:
		if n > 0 && n < 1000 {
			return int(n)
		}
	case string:
		s := strings.TrimSpace(n)
		if len(s) == 3 && s[0] >= '1' && s[0] <= '5' {
			if d, ok := smallInt(s); ok {
				return d
			}
		}
	}
	return 0
}

// smallInt parses a strictly 3-digit all-numeric string.
func smallInt(s string) (int, bool) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}

// statusToCode maps the Google-style status strings only when they are HTTP
// statuses in their own right; "RESOURCE_EXHAUSTED" is 429 and "UNAVAILABLE"
// is 503, so BodyErrorCode can surface a rate limit buried inside a wrapper
// 5xx. The DECODE path (ClassifyDetail) uses the wider statusFromCodeAndStatus,
// which also knows the non-429 Google statuses.
func statusToCode(status string) int {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "RESOURCE_EXHAUSTED":
		return 429
	case "UNAVAILABLE":
		return 503
	case "DEADLINE_EXCEEDED":
		return 504
	default:
		return 0
	}
}

// rateLimitWrappedPhrases catch a non-429 response that still throttles.
// Each is a provider's way of saying "this is a quota condition" in prose
// while answering with a 5xx or a 2xx envelope.
var rateLimitWrappedPhrases = []string{
	"resource exhausted",
	"resource_exhausted",
	"request too large",
	"service tier capacity exceeded",
	"quota exceeded",
}

// LooksLikeRateLimitWrapped reports whether a response that is not a 429
// actually carries a rate-limit signal: Vertex/Gemini "Resource exhausted"
// inside a 5xx, OpenAI "Request too large" TPM rejections, Mistral "service
// tier capacity exceeded". A caller that only trusts the HTTP status would
// retry such a response as a transient 5xx when the right move is to cool the
// account down.
func LooksLikeRateLimitWrapped(body []byte) bool {
	if BodyErrorCode(body) == 429 {
		return true
	}
	return containsAny(strings.ToLower(string(body)), rateLimitWrappedPhrases)
}

// MaxRetryAfterHeader caps upstream Retry-After hints taken from headers. A
// provider that answers with a day-long Retry-After would otherwise park the
// account for a day on a single response; anything beyond an hour is treated
// as "an hour, then probe again". Calendar-derived quota resets computed by
// the router itself are not subject to this cap.
const MaxRetryAfterHeader = time.Hour

// CapRetryAfter clamps an upstream-suggested wait to MaxRetryAfterHeader.
func CapRetryAfter(d time.Duration) time.Duration {
	if d > MaxRetryAfterHeader {
		return MaxRetryAfterHeader
	}
	return d
}
