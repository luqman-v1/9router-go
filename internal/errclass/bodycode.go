package errclass

import (
	"bytes"
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

// LooksLikeCreditsExhausted reports whether an error body reads as a spent
// credit/quota budget rather than a transient throttle: OpenAI
// "insufficient_quota", Anthropic "credit balance is too low", new-api family
// gateways "insufficient_user_quota", DeepSeek "insufficient balance", a
// command-code/bai "insufficient credits". The vocabulary matches the quota
// rules the connection-rotation classifier already parks on.
func LooksLikeCreditsExhausted(body string) bool {
	return containsAny(strings.ToLower(body), creditsExhaustedPhrases)
}

var creditsExhaustedPhrases = []string{
	"insufficient_quota",
	"billing_hard_limit_reached",
	"exceeded your current quota",
	"exceeded your current usage quota",
	"credit_balance_too_low",
	"credit_balance_exhausted",
	"your credit balance is too low",
	"credit balance is too low",
	"credits exhausted",
	"credit exhausted",
	"exhausted all your credits",
	"out of credits",
	"out of funds",
	"insufficient_user_quota",
	"insufficient balance",
	"insufficient_balance",
	"insufficient credit",
	"insufficient credit balance",
	"no remaining credits",
	"payment required",
	"quota exceeded",
	"insufficient quota",
	"exceeded quota",
	"exceed quota",
	"monthly limit",
	"monthly quota",
	"per month limit",
	"per-month limit",
	"frequency limit",
	"free allowance",
	"billing cap",
	"balance=0",
	"individual quota reached",
	"enable overages",
	"billing required",
	"tier has been exhausted",
	"free tier of the model has been exhausted",
	"hard limit",
	"plan limit",
	"余额不足",
	"无可用资源包",
}

// LooksLikeCreditErrorCode reports whether a body's structured error code is a
// credit code. That is the one signal that cannot have come from the caller's
// own input: a 400 may echo the request text, but a code is the upstream's own
// verdict. It reads the same two fields BodyErrorCode does — error.code, then
// code — and ignores a numeric code, which says nothing about billing.
//
// A caller that already holds the code (providers.extractErrorText folds it into
// the error text) uses LooksLikeCreditErrorCodeString instead of wrapping prose
// in a JSON envelope.
func LooksLikeCreditErrorCode(body []byte) bool {
	body = bytes.TrimSpace(body)
	if len(body) == 0 || body[0] != '{' {
		return false
	}
	var env struct {
		Code  any `json:"code"`
		Error *struct {
			Code any `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &env) != nil {
		return false
	}
	return isCreditErrorCode(env.Code) || (env.Error != nil && isCreditErrorCode(env.Error.Code))
}

func isCreditErrorCode(v any) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	return LooksLikeCreditErrorCodeString(s)
}

// LooksLikeCreditErrorCodeString is LooksLikeCreditErrorCode for a caller that
// already extracted the code from the envelope, so no re-encoding is needed. It
// matches the code alone, not a substring of it: a code embedded in a longer
// string is prose, and prose at a 400 can be the caller's own words.
func LooksLikeCreditErrorCodeString(code string) bool {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "insufficient_user_quota", "insufficient_quota", "insufficient_balance",
		"billing_hard_limit_reached", "credit_balance_too_low", "credit_balance_exhausted":
		return true
	}
	return false
}

// creditsExhaustedForStatus decides whether a credit reading is admitted for the
// status it arrived with. Every status except 400 keeps the plain prose match: a
// 402 is a spent balance by definition, and a 429 that says "insufficient
// balance" is one too.
//
// A 400 is how an upstream rejects the CALLER's input, and many quote that input
// back in error.message ("Unknown parameter: '<x>'", "Invalid value '<x>' for
// model"). A credit phrase inside such an echo is request text, not a billing
// verdict — treating it as one parked a healthy shared connection for every user
// until the recovery tick cleared it. So on 400 prose alone never counts.
//
// The structured credit code is the other carrier, and it counts on its own: a
// code is the upstream's own verdict, never an echo. That means the reading no
// longer needs a prose hit as well — openai, azure and the new-api family answer
// a spent balance with a 400 whose message is terse or localized ("リクエストの
// 上限に達しました", "saldo tidak mencukupi"), and requiring a phrase from the
// table silently left every code but "insufficient_quota" request-scoped.
func creditsExhaustedForStatus(status int, body string, structured []byte) bool {
	if status == 400 {
		return LooksLikeCreditErrorCode([]byte(body)) || LooksLikeCreditErrorCode(structured)
	}
	return LooksLikeCreditsExhausted(body)
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
