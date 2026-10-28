package providers

import (
	"math"
	"net/http"
	"strings"
)

// ErrorRule classifies an upstream error by text match or status code.
// Checked top-to-bottom: text rules first, then status rules.
type ErrorRule struct {
	Text       string // substring match (case-insensitive); empty means not text-based
	Status     int    // HTTP status code match; 0 means not status-based
	CooldownMs int    // fixed cooldown duration; 0 means use exponential backoff
	Backoff    bool   // true = use exponential backoff (rate limit)
	// Provider scopes a text rule to one canonical provider id; empty applies
	// to every provider.
	Provider string
}

// BackoffConfig controls exponential backoff scaling.
var BackoffConfig = struct {
	BaseMs   int
	MaxMs    int
	MaxLevel int
}{
	BaseMs:   2000,          // 2 seconds base
	MaxMs:    5 * 60 * 1000, // 5 minutes cap
	MaxLevel: 15,
}

// TransientCooldownMs is the default cooldown for unmatched/unknown errors.
const TransientCooldownMs = 30 * 1000 // 30 seconds

// cooldown durations (ms) used by ERROR_RULES
const (
	cooldownLong  = 2 * 60 * 1000 // 2 minutes
	cooldownShort = 5 * 1000      // 5 seconds

	// cooldownBannedAccount parks a deepseek-web account whose credential the
	// site refuses (40012 USER_IS_BANNED). The ban names the account, not a
	// model and not a transient condition: the generic 2-minute 403 rule
	// would re-pick the punished account every two minutes, and each attempt
	// is another request the site is actively penalizing.
	cooldownBannedAccount = 24 * 60 * 60 * 1000

	// cooldownAddressRefused is the pause for a deepseek-web address-level
	// refusal (40029 IP_ACCESS_RESTRICTED). The account is fine; the egress
	// is not, so the account waits out a short window instead of absorbing
	// every turn the same address is refused on.
	cooldownAddressRefused = 30 * 60 * 1000
)

// ErrorRules is the ordered list of error classification rules, matching Next.js ERROR_RULES.
// Checked top-to-bottom: text rules first (by order), then status rules.
var ErrorRules = []ErrorRule{
	// --- Text-based rules (checked first, order = priority) ---
	// Intl gateway blocks affect the account/IP, so let the connection back
	// off (upstream 3b137aa6): without a provider-scoped rule the messages
	// fall through to the request-scoped 4xx path and the connection gets
	// zero cooldown while its backoff level never moves.
	{Provider: "codebuddy-intl", Text: "request illegal", Backoff: true},
	{Provider: "codebuddy-intl", Text: "conversation id missing", Backoff: true},
	// DeepSeek web account-level refusals both arrive as 403, and they are
	// not the same verdict. A ban ("refused this account") is the account
	// itself; an address refusal ("refused this address") is the egress, and
	// the account should be tried from another address rather than parked
	// forever. The generic 403 rule cannot tell them apart.
	{Provider: "deepseek-web", Text: "refused this account", CooldownMs: cooldownBannedAccount},
	{Provider: "deepseek-web", Text: "refused this address", CooldownMs: cooldownAddressRefused},
	{Text: "no credentials", CooldownMs: cooldownLong},
	{Text: "request not allowed", CooldownMs: cooldownShort},
	{Text: "improperly formed request", CooldownMs: cooldownLong},
	{Text: "rate limit", Backoff: true},
	{Text: "too many requests", Backoff: true},
	{Text: "quota exceeded", Backoff: true},
	// Out-of-credits arrives as a 400 from several providers ("insufficient
	// credits" / Anthropic's "credit balance is too low"). The combo advance
	// happens through the request-scoped 4xx path, but without these rules
	// the spent account reads as healthy and is retried on every pass
	// (upstream b82f5c4a).
	{Text: "insufficient credit", Backoff: true},
	{Text: "credit balance is too low", Backoff: true},
	{Text: "capacity", Backoff: true},
	{Text: "overloaded", Backoff: true},
	{Text: "resource_exhausted", Backoff: true},
	{Text: "resource has been exhausted", Backoff: true},
	{Text: "model_capacity_exhausted", Backoff: true},
	{Text: "server is temporarily unavailable", Backoff: true},

	// --- Status-based rules (fallback when text doesn't match) ---
	{Status: 401, CooldownMs: cooldownLong},
	{Status: 402, CooldownMs: cooldownLong},
	{Status: 403, CooldownMs: cooldownLong},
	{Status: 404, CooldownMs: cooldownLong},
	{Status: 429, Backoff: true},
	{Status: 502, Backoff: true},
	{Status: 503, Backoff: true},
	{Status: 504, Backoff: true},
}

// ModelFitErrorPatterns are the upstream messages that mean "this model cannot
// hold the request", as opposed to "this account cannot serve it". Matched
// case-insensitively against the error body.
//
// The distinction matters inside a combo: an account fault is worth another
// connection, but a prompt that does not fit the model gets the same answer from
// every connection to it, so the only useful move is a member with a bigger
// context window (#338).
var ModelFitErrorPatterns = []string{
	"context_length_exceeded",
	"maximum context length",
	"prompt is too long",
	"reduce the length of the messages",
	"input is too long",
	"too many tokens",
	"context window",
	"exceeds the maximum number of tokens",
	// A text-only model rejects the media an older turn still carries. The
	// trailing turn was text, so the request never registered as a vision
	// request and the history went out verbatim; every connection to this
	// model answers the same way, so like the length patterns above the only
	// useful move is the next member. The quoted spellings both matter: the
	// body is matched raw, so a JSON-encoded message carries the quotes as
	// \" while a plain-text error body carries them bare.
	`unsupported value \"image`,
	`unsupported value "image`,
	"not support image",
	"image input not supported",
	"images are not supported",
}

// ModelFitStatusCodes are the statuses an upstream uses to reject an oversized
// request. A 400 carries it almost everywhere; 413 and 422 are the other two
// spellings seen in the wild, and Zed sends it as a 403. 403 is otherwise the
// account-refusal status, so admitting it here means every 403 is re-checked
// against the body patterns before it can be called a fit failure — a plain
// permission refusal still lands on the generic 403 rule. Gating on the status
// keeps a provider's prose from triggering the classification on an unrelated
// failure.
var ModelFitStatusCodes = map[int]bool{
	http.StatusBadRequest:            true, // 400
	http.StatusRequestEntityTooLarge: true, // 413
	http.StatusUnprocessableEntity:   true, // 422
	http.StatusForbidden:             true, // 403, Zed
}

// IsModelFitError reports whether an upstream failure means the chosen model is
// too small for the request rather than the connection being at fault.
func IsModelFitError(statusCode int, body []byte) bool {
	if !ModelFitStatusCodes[statusCode] || len(body) == 0 {
		return false
	}
	text := strings.ToLower(string(body))
	for _, pattern := range ModelFitErrorPatterns {
		if strings.Contains(text, pattern) {
			return true
		}
	}
	return false
}

// GetQuotaCooldown calculates exponential backoff cooldown for rate limits.
// Level 0 → 2s, Level 1 → 2s, Level 2 → 4s, Level 3 → 8s, ... capped at MaxMs.
func GetQuotaCooldown(backoffLevel int) int {
	level := max(backoffLevel-1, 0)
	cooldown := int(float64(BackoffConfig.BaseMs) * math.Pow(2, float64(level)))
	return min(cooldown, BackoffConfig.MaxMs)
}

// ErrorClassification holds the result of ClassifyError.
type ErrorClassification struct {
	ShouldFallback  bool
	CooldownMs      int
	NewBackoffLevel int // only meaningful when the matched rule has Backoff=true
}

// ClassifyError classifies an upstream error by matching text and status against ErrorRules.
// Returns the cooldown duration and new backoff level.
// Matches Next.js checkFallbackError() in open-sse/services/accountFallback.js.
func ClassifyError(statusCode int, errorText string, backoffLevel int) ErrorClassification {
	return ClassifyErrorForProvider(statusCode, errorText, backoffLevel, "")
}

// ClassifyErrorForProvider is ClassifyError with provider-scoped rules
// applied: provider is the canonical provider id the request routed to
// (aliases are resolved here, so `cbai` matches the codebuddy-intl rules).
func ClassifyErrorForProvider(statusCode int, errorText string, backoffLevel int, provider string) ErrorClassification {
	if provider != "" {
		provider = ResolveAlias(provider)
	}
	lowerError := ""
	if errorText != "" {
		lowerError = strings.ToLower(errorText)
	}

	for _, rule := range ErrorRules {
		// Text-based match (substring, case-insensitive)
		if rule.Text != "" && lowerError != "" && strings.Contains(lowerError, rule.Text) {
			if rule.Provider != "" && rule.Provider != provider {
				continue
			}
			if rule.Backoff {
				newLevel := min(backoffLevel+1, BackoffConfig.MaxLevel)
				return ErrorClassification{
					ShouldFallback:  true,
					CooldownMs:      GetQuotaCooldown(newLevel),
					NewBackoffLevel: newLevel,
				}
			}
			return ErrorClassification{
				ShouldFallback:  true,
				CooldownMs:      rule.CooldownMs,
				NewBackoffLevel: backoffLevel,
			}
		}

		// Status-based match
		if rule.Status != 0 && rule.Status == statusCode {
			if rule.Backoff {
				newLevel := min(backoffLevel+1, BackoffConfig.MaxLevel)
				return ErrorClassification{
					ShouldFallback:  true,
					CooldownMs:      GetQuotaCooldown(newLevel),
					NewBackoffLevel: newLevel,
				}
			}
			return ErrorClassification{
				ShouldFallback:  true,
				CooldownMs:      rule.CooldownMs,
				NewBackoffLevel: backoffLevel,
			}
		}
	}
	// Upstream parity (open-sse/services/accountFallback.js checkFallbackError):
	// request-scoped 4xx that match no rule say nothing about the credential,
	// so the account must not be cooled down. Account-scoped statuses keep
	// their rules above (401/402/403/404/429 + quota/capacity text rules).
	if statusCode >= 400 && statusCode < 500 && statusCode != 401 && statusCode != 402 && statusCode != 403 && statusCode != 429 {
		return ErrorClassification{ShouldFallback: false}
	}

	// Default: transient cooldown for any unmatched error
	return ErrorClassification{
		ShouldFallback:  true,
		CooldownMs:      TransientCooldownMs,
		NewBackoffLevel: backoffLevel,
	}
}
