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
	// Quota marks an error that means the account's credit/billing budget is
	// spent. These do NOT clear on a short timer, so they get a long cooldown
	// and are surfaced as a distinct status instead of a transient blip.
	Quota bool
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

	// QuotaExhaustedCooldownMs is how long an account is parked after the
	// upstream reports its credit/quota budget is spent. Ported from the
	// upstream classify429 helper (QUOTA_EXHAUSTED_COOLDOWN_MS): a rate limit
	// clears in a minute, but "insufficient balance" will not fix itself in 30
	// seconds — retrying that often just burns a guaranteed-failed upstream
	// call and keeps the dashboard showing a false recovery.
	QuotaExhaustedCooldownMs = 60 * 60 * 1000 // 1 hour
)

// ErrorRules is the ordered list of error classification rules, matching Next.js ERROR_RULES.
// Checked top-to-bottom: text rules first (by order), then status rules.
var ErrorRules = []ErrorRule{
	// --- Quota / credit exhaustion (checked before generic rules) ---
	// A spent budget is not a transient throttle: give it the long cooldown
	// instead of the 30s transient default. Without this, "insufficient
	// balance" heals itself every 30 seconds and the dashboard keeps
	// claiming a guaranteed-failing account is connected.
	{Text: "insufficient balance", CooldownMs: QuotaExhaustedCooldownMs, Quota: true},
	{Text: "insufficient credit", CooldownMs: QuotaExhaustedCooldownMs, Quota: true},
	{Text: "credits exhausted", CooldownMs: QuotaExhaustedCooldownMs, Quota: true},
	{Text: "credit exhausted", CooldownMs: QuotaExhaustedCooldownMs, Quota: true},
	{Text: "no remaining credits", CooldownMs: QuotaExhaustedCooldownMs, Quota: true},
	{Text: "quota exceeded", CooldownMs: QuotaExhaustedCooldownMs, Quota: true},
	{Text: "insufficient quota", CooldownMs: QuotaExhaustedCooldownMs, Quota: true},
	{Text: "monthly limit", CooldownMs: QuotaExhaustedCooldownMs, Quota: true},
	{Text: "frequency limit", CooldownMs: QuotaExhaustedCooldownMs, Quota: true},
	{Text: "payment required", CooldownMs: QuotaExhaustedCooldownMs, Quota: true},
	// TokenHarbor's free tier answers 429 with "You've used this period's free
	// allowance. Your next rolling 7-day period starts on ...". The status
	// alone reads as a throttle, so it only ever earned a 2s→5min backoff and
	// the spent account was re-dialled all window. The allowance is spent for
	// days, which is what this cooldown family is for.
	{Text: "free allowance", CooldownMs: QuotaExhaustedCooldownMs, Quota: true},

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
	// "credit balance is too low" stays here: it is Anthropic's wording for a
	// spent balance but does not contain "insufficient credit", so the head
	// quota rules above do not match it.
	{Text: "credit balance is too low", CooldownMs: QuotaExhaustedCooldownMs, Quota: true},
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

// quotaExhaustedPatterns mirror the upstream classify429 QUOTA_EXHAUSTED_PATTERNS
// (plus the credit-balance wording gateways actually return). Matched
// case-insensitively against the error body.
var quotaExhaustedPatterns = []string{
	"monthly limit",
	"monthly quota",
	"per month limit",
	"per-month limit",
	"quota exceeded",
	"exceeded quota",
	"exceed quota",
	"exceeded your current quota",
	"insufficient quota",
	"insufficient balance",
	"insufficient credit",
	"billing cap",
	"credit exhausted",
	"credits exhausted",
	"out of credits",
	"no remaining credit",
	"hard limit",
	"plan limit",
	"resource exhausted",
	"individual quota reached",
	"enable overages",
	"billing required",
	"payment required",
	"balance=0",
	// CodeBuddy words a spent model quota as a frequency limit and states when
	// it resets: "usage exceeds frequency limit ... reset at <timestamp>". It is
	// not a transient throttle, so it belongs with the exhausted budgets rather
	// than the 2-second backoff floor.
	"frequency limit",
}

// LooksLikeQuotaExhausted reports whether an error body reads as a spent
// credit/quota budget rather than a transient failure. Exposed so the
// dashboard can classify stored errors (which predate any live request) with
// the same vocabulary the request path uses.
func LooksLikeQuotaExhausted(errorText string) bool {
	if errorText == "" {
		return false
	}
	lower := strings.ToLower(errorText)
	for _, pat := range quotaExhaustedPatterns {
		if strings.Contains(lower, pat) {
			return true
		}
	}
	return false
}

// rateLimitPatterns mark a throttle: the account is fine, it is being asked to
// slow down. Kept separate from quota exhaustion because the remedy differs --
// one waits, the other tops up. Matched case-insensitively against the error.
var rateLimitPatterns = []string{
	"rate limit",
	"rate_limit",
	"ratelimit",
	"too many requests",
	"resource_exhausted",
	"resource has been exhausted",
	"requests per minute",
	"rpm limit",
	"tpm limit",
	"tokens per minute",
	"requests per day",
	"rpd limit",
	"429",
}

// permanentFailurePatterns mark errors that will not clear by waiting: the
// credential is rejected, revoked, or points at something gone. Distinct from
// quota (needs money) and throttle (needs time) -- this needs the operator to
// fix the key.
var permanentFailurePatterns = []string{
	"invalid api key",
	"invalid_api_key",
	"invalid key",
	"apikey is invalid",
	"incorrect api key",
	"unauthorized",
	"authentication_error",
	"authentication failed",
	"permission denied",
	"access denied",
	"not authorized",
	"invalid_grant",
	"invalid_token",
	"token expired",
	"token has expired",
	"revoked",
	"no credentials",
	"account suspended",
	"account disabled",
	"not found",
}

// LooksLikePermanentFailure reports whether an error means the credential is
// dead rather than temporarily unusable. Used by the dashboard so a rejected
// key is not silently reported as recovered once its cooldown lapses.
func LooksLikePermanentFailure(errorText string) bool {
	if errorText == "" {
		return false
	}
	lower := strings.ToLower(errorText)
	for _, pat := range permanentFailurePatterns {
		if strings.Contains(lower, pat) {
			return true
		}
	}
	return false
}

// throttleWindowPatterns name an explicit short window (per minute, per second,
// rpm/tpm). These are throttles no matter what other words they carry: "quota
// exceeded per minute" is a rate limit, not a spent balance, and telling the
// operator to top up an account that only needs to wait is wrong advice.
var throttleWindowPatterns = []string{
	"per minute",
	"per-minute",
	"per second",
	"per-second",
	"per hour",
	"requests per minute",
	"requests per second",
	"tokens per minute",
	"tokens per second",
	"rpm limit",
	"tpm limit",
	"rps limit",
	"too many requests",
}

// LooksLikeThrottleWindow reports whether an error names a short rolling window
// that resets on its own. The dashboard uses it to keep such a message out of
// the quota-exhausted bucket, whose advice ("top up the account") would be
// wrong. Deliberately NOT applied to the request-path quota cooldown: parking a
// per-minute limit for an hour is wasteful, but mis-parking a real balance
// exhaustion for 30 seconds is worse, and that path already matches the
// narrower rules in ErrorRules.
func LooksLikeThrottleWindow(errorText string) bool {
	if errorText == "" {
		return false
	}
	lower := strings.ToLower(errorText)
	for _, pat := range throttleWindowPatterns {
		if strings.Contains(lower, pat) {
			return true
		}
	}
	return false
}

// LooksLikeRateLimited reports whether an error body reads as a throttle rather
// than a dead account. Mirrors LooksLikeQuotaExhausted for the dashboard's
// stored-error path.
func LooksLikeRateLimited(errorText string) bool {
	if errorText == "" {
		return false
	}
	lower := strings.ToLower(errorText)
	for _, pat := range rateLimitPatterns {
		if strings.Contains(lower, pat) {
			return true
		}
	}
	return false
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
