package providers

import (
	"encoding/json"
	"net/http"
	"strings"

	"9router/proxy/internal/errclass"
)

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

// LooksLikeCreditCode reports whether a raw upstream body carries a structured
// credit code (error.code or code). A code is the upstream's own verdict and
// cannot be the caller's input quoted back, which is why it is the only signal
// that admits a quota reading on an HTTP 400.
func LooksLikeCreditCode(body []byte) bool {
	return errclass.LooksLikeCreditErrorCode(body)
}

// LooksLikeCreditCodeText reports whether already-extracted error text is one of
// the six structured credit codes (chat.extractErrorText folds error.code into
// the text it hands the classifier). The body-based variant reads the envelope
// itself; a caller holding only the extracted text needs this one, and matching
// the whole string is what keeps a 400 that merely quotes the phrase as request
// text out of the quota bucket.
func LooksLikeCreditCodeText(errorText string) bool {
	return errclass.LooksLikeCreditErrorCodeString(errorText)
}

// creditCodeIn returns the structured credit code carried by the body or already
// folded into the extracted text, and "" when neither carries one.
func creditCodeIn(body []byte, errorText string) string {
	for _, v := range creditCodeValues(body, errorText) {
		if v != "" {
			return v
		}
	}
	return ""
}

// creditCodeValues lists the code carriers, body envelope first.
func creditCodeValues(body []byte, errorText string) []string {
	var env struct {
		Code  any `json:"code"`
		Error *struct {
			Code any `json:"code"`
		} `json:"error"`
	}
	var fromBody []string
	if len(body) > 0 && json.Unmarshal(body, &env) == nil {
		if env.Error != nil {
			fromBody = append(fromBody, codeString(env.Error.Code))
		}
		fromBody = append(fromBody, codeString(env.Code))
	}
	return append(fromBody, extractedCode(errorText))
}

// extractedCode picks the structured code out of extracted error text. Only a
// text that IS the code counts — chat.extractErrorText appends the code to the
// message, but anything else here is prose, and a code quoted inside prose is
// the caller's own words rather than the upstream's verdict.
func extractedCode(errorText string) string {
	if LooksLikeCreditCodeText(errorText) {
		return strings.TrimSpace(errorText)
	}
	return ""
}

// codeString renders a code that is a JSON string and nothing else. A numeric
// code is an HTTP status, not a billing verdict.
func codeString(v any) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	if LooksLikeCreditCodeText(s) {
		return strings.TrimSpace(s)
	}
	return ""
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
	// Antigravity ToS bans never clear by waiting.
	"this service has been disabled in this account",
	"tos_violation",
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
