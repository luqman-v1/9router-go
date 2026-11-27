package chat

import (
	"9router/proxy/internal/log"
	"9router/proxy/internal/translator"
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func resolveProtocol(endpoint string) string {
	if endpoint == "/v1/messages" {
		return "Claude-Messages"
	}
	if strings.Contains(endpoint, "gemini") {
		return "Gemini"
	}
	return "OpenAI-Chat"
}

func resolveCacheSource(u *translator.OpenAIUsage) string {
	if u != nil && (u.CachedTokens > 0 || (u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens > 0)) {
		return "Upstream (Provider)"
	}
	return "None"
}

// extractErrorText attempts to extract a human-readable error message from an upstream error JSON body.
// Returns "" when the body isn't parseable or has no message field.
func extractErrorText(body []byte) string {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
			Code    any    `json:"code"`
			Status  string `json:"status"`
			Details []struct {
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
		Message string `json:"message"`
		Code    any    `json:"code"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil {
		var parts []string
		if parsed.Error.Message != "" {
			parts = append(parts, parsed.Error.Message)
		} else if parsed.Message != "" {
			parts = append(parts, parsed.Message)
		}
		if parsed.Error.Status != "" {
			parts = append(parts, parsed.Error.Status)
		}
		for _, d := range parsed.Error.Details {
			if d.Reason != "" {
				parts = append(parts, d.Reason)
			}
		}
		// The structured code is part of what the classifier reads: a 400 is
		// only a spent balance when the upstream's own code says so, and that
		// code rides in error.code, not in the prose (providers.ClassifyError).
		if code := creditErrorCodeString(parsed.Error.Code, parsed.Code); code != "" {
			parts = append(parts, code)
		}
		if len(parts) > 0 {
			return strings.Join(parts, " ")
		}
	}
	trimmed := bytes.TrimSpace(body)
	if bytes.HasPrefix(trimmed, []byte("<!DOCTYPE html")) || bytes.HasPrefix(trimmed, []byte("<html")) {
		lower := strings.ToLower(string(trimmed))
		if strings.Contains(lower, "cloudflare") || strings.Contains(lower, "attention required") {
			return "Cloudflare WAF challenge (Attention Required!): check User-Agent or network proxy"
		}
		if titleStart := strings.Index(lower, "<title>"); titleStart != -1 {
			titleEnd := strings.Index(lower[titleStart:], "</title>")
			if titleEnd != -1 {
				return "upstream returned HTML: " + strings.TrimSpace(string(trimmed[titleStart+7:titleStart+titleEnd]))
			}
		}
		return "upstream returned HTML error page"
	}
	return ""
}

// creditErrorCodeString renders a structured error code for the classifier. A
// numeric code says nothing about billing and a non-string is not a code, so
// both yield "". error.code wins over the top-level code, the same precedence
// providers.BodyErrorCode uses.
func creditErrorCodeString(codes ...any) string {
	for _, v := range codes {
		if s, ok := v.(string); ok {
			if s = strings.TrimSpace(s); s != "" {
				return s
			}
		}
	}
	return ""
}

var (
	resetsInRegex           = regexp.MustCompile(`(?i)resets?\s+in\s+([0-9hms\.]+)`)
	codebuddyResetDateRegex = regexp.MustCompile(`(?i)(\d{4}-\d{2}-\d{2})\s+(\d{2}:\d{2}:\d{2})(?:\s*UTC\+?([0-9:]+))?`)
)

const (
	minResetCooldown = 5 * time.Second
	maxResetCooldown = 2 * time.Hour
)

// extractResetDuration attempts to extract a structured reset duration from an error payload.
// It parses:
// 1. Google RPC ErrorInfo metadata: quotaResetDelay ("1h12m28.109534319s")
// 2. Text message patterns: "Resets in 1h12m28s."
// 3. Clamps duration between 5 seconds and 2 hours to prevent deadlock / indefinite lockout.
func extractResetDuration(body []byte) (time.Duration, bool) {
	if len(body) == 0 {
		return 0, false
	}

	// 1. Google RPC error details
	var rpcErr struct {
		Error struct {
			Message string `json:"message"`
			Details []struct {
				Metadata map[string]string `json:"metadata"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &rpcErr); err == nil {
		for _, d := range rpcErr.Error.Details {
			if delayStr, ok := d.Metadata["quotaResetDelay"]; ok && delayStr != "" {
				delayStr = strings.TrimRight(delayStr, ".")
				if dur, err := time.ParseDuration(delayStr); err == nil && dur > 0 {
					return clampResetDuration(dur), true
				}
			}
		}
		if rpcErr.Error.Message != "" {
			if matches := resetsInRegex.FindStringSubmatch(rpcErr.Error.Message); len(matches) > 1 {
				raw := strings.TrimRight(matches[1], ".")
				if dur, err := time.ParseDuration(raw); err == nil && dur > 0 {
					return clampResetDuration(dur), true
				}
			}
		}
	}

	// 2. Fallback regex on raw body string
	if matches := resetsInRegex.FindSubmatch(body); len(matches) > 1 {
		raw := strings.TrimRight(string(matches[1]), ".")
		if dur, err := time.ParseDuration(raw); err == nil && dur > 0 {
			return clampResetDuration(dur), true
		}
	}

	// 3. CodeBuddy date-time pattern (e.g. 2026-09-28 14:30:00 or with UTC offset)
	if matches := codebuddyResetDateRegex.FindSubmatch(body); len(matches) > 2 {
		dateStr := string(matches[1])
		timeStr := string(matches[2])
		tzOffset := "+08:00"
		if len(matches) > 3 && len(matches[3]) > 0 {
			tz := string(matches[3])
			if !strings.Contains(tz, ":") {
				if num, err := strconv.Atoi(tz); err == nil {
					tzOffset = fmt.Sprintf("+%02d:00", num)
				}
			} else if !strings.HasPrefix(tz, "+") && !strings.HasPrefix(tz, "-") {
				tzOffset = "+" + tz
			} else {
				tzOffset = tz
			}
		}
		if tm, err := time.Parse(time.RFC3339, fmt.Sprintf("%sT%s%s", dateStr, timeStr, tzOffset)); err == nil {
			dur := time.Until(tm)
			if dur > 0 {
				return clampResetDuration(dur), true
			}
		}
	}

	return 0, false
}

func clampResetDuration(dur time.Duration) time.Duration {
	if dur < minResetCooldown {
		return minResetCooldown
	}
	if dur > maxResetCooldown {
		return maxResetCooldown
	}
	return dur
}

// extractRetryAfter extracts a retryAfter ISO timestamp from an upstream error JSON body.
// Checks quotaResetDelay, "Resets in X", or common field names: retryAfter, retry_after, resetsAt, resets_at.
// Returns "" when not found or not parseable.
func extractRetryAfter(body []byte) string {
	if dur, ok := extractResetDuration(body); ok {
		return time.Now().UTC().Add(dur).Format(time.RFC3339)
	}
	var parsed struct {
		RetryAfter string `json:"retryAfter"`
		RetryAlt   string `json:"retry_after"`
		ResetsAt   string `json:"resetsAt"`
		ResetsAlt  string `json:"resets_at"`
		Error      struct {
			RetryAfter string `json:"retryAfter"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	if parsed.RetryAfter != "" {
		return parsed.RetryAfter
	}
	if parsed.RetryAlt != "" {
		return parsed.RetryAlt
	}
	if parsed.ResetsAt != "" {
		return parsed.ResetsAt
	}
	if parsed.ResetsAlt != "" {
		return parsed.ResetsAlt
	}
	if parsed.Error.RetryAfter != "" {
		return parsed.Error.RetryAfter
	}
	return ""
}

// formatRetryAfter formats an ISO timestamp into a human-readable "reset after Xm Ys" string.
// Returns "" when the timestamp is empty, unparseable, or in the past.
func formatRetryAfter(isoTimestamp string) string {
	if isoTimestamp == "" {
		return ""
	}
	parsed, err := time.Parse(time.RFC3339, isoTimestamp)
	if err != nil {
		return ""
	}
	// time.Until is a Duration in nanoseconds. Dividing by 1000 yielded
	// milliseconds, so a 150s cooldown read as "reset after 41399h" and
	// contradicted the Retry-After header sent alongside it.
	remaining := time.Until(parsed)
	if remaining <= 0 {
		return "reset after 0s"
	}
	totalSec := int((remaining + time.Second - 1) / time.Second) // ceil
	h := totalSec / 3600
	m := (totalSec % 3600) / 60
	s := totalSec % 60
	var parts []string
	if h > 0 {
		parts = append(parts, fmt.Sprintf("%dh", h))
	}
	if m > 0 {
		parts = append(parts, fmt.Sprintf("%dm", m))
	}
	if s > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%ds", s))
	}
	return "reset after " + strings.Join(parts, " ")
}

// claudeSessionIDFromBody reads the session id the cloak wrote into
// metadata.user_id, so it can be echoed in x-claude-code-session-id.
func claudeSessionIDFromBody(body []byte) string {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}
	meta, _ := req["metadata"].(map[string]any)
	if meta == nil {
		return ""
	}
	userID, _ := meta["user_id"].(string)
	return extractClaudeSessionIdFromUserId(userID)
}

func isAccountAuthFailure(lower string) bool {
	return strings.Contains(lower, "no credentials") ||
		strings.Contains(lower, "invalid_grant") ||
		strings.Contains(lower, "invalid token") ||
		strings.Contains(lower, "token type is not supported") ||
		strings.Contains(lower, "authentication expired") ||
		strings.Contains(lower, "token expired") ||
		strings.Contains(lower, "account suspended") ||
		strings.Contains(lower, "account disabled") ||
		strings.Contains(lower, "incorrect api key") ||
		strings.Contains(lower, "invalid api key") ||
		strings.Contains(lower, "organization is not supported") ||
		strings.Contains(lower, "insufficient funds for organization")
}

// isModelQuotaText reports a refusal that names one model's quota rather than
// the account's standing. These lock the model, not the connection: a Qoder
// account whose paid models are out of credits still serves its free ones, and
// parking the whole connection answered `dfmodel` with "all in cooldown" after
// a single `dmodel` refusal.
func isModelQuotaText(errorText string) bool {
	upper := strings.ToUpper(errorText)
	return strings.Contains(upper, "QUOTA_EXHAUSTED") ||
		strings.Contains(errorText, "Individual quota reached") ||
		strings.Contains(upper, "MODEL_CAPACITY_EXHAUSTED") ||
		// Qoder's billing envelope: code 110/112 or a pricingUrl field
		// (qoderIsBillingBlock). The refusal is per model — the account's
		// free models keep working while a paid one is out of credits.
		strings.Contains(upper, "PRICINGURL") ||
		strings.Contains(upper, "PRICING_URL") ||
		// Qoder's queue window (qoderQueuedTimeout): the model is briefly out
		// of capacity, which throttles that model alone. Locking the account
		// here parked every other model on it behind one busy queue.
		strings.Contains(upper, "STILL QUEUED")
}

func mentionsGate(lower string) bool {
	return strings.Contains(lower, "is not supported") ||
		strings.Contains(lower, "not supported") ||
		strings.Contains(lower, "model access is disabled") ||
		strings.Contains(lower, "endpoint is unavailable")
}

// isModelScopedError reports whether an upstream retryable error is
// specific to the requested model (e.g. 429 model quota, 402 insufficient funds
// for paid models, or 401 "model is not supported") rather than an account-scoped
// credential failure. For model-scoped errors, only LockConnectionModel and
// RecordConnectionError are set so that unrelated healthy models on the same account
// remain available.
// isOpencodeFreeTierRefusal reports whether an upstream failure is the OpenCode
// free-tier gate refusing to serve this traffic, rather than a problem with the
// account, key or request.
//
// The gate answers in two shapes, and both mean the same thing — it is not
// serving this egress at that moment:
//
//   - 403 with a body whose type is FreeTierError, when the request does not
//     look like the official client.
//   - 429 with a body whose type is FreeUsageLimitError ("Free usage limit
//     reached. Subscribe to OpenCode Go to keep going"), when the free budget
//     accounted to this egress is spent.
//
// Neither says anything about the caller's credentials, and both are read as a
// verdict on the request by every client — so neither may be forwarded verbatim;
// see opencodeClosureError. The 429 in particular must not cool the account
// down: the account is healthy, the egress is not.
//
// The provider check keeps every other provider's 403/429 handling untouched,
// and the body check keeps a genuine OpenCode auth or billing error on the
// normal path.
func isOpencodeFreeTierRefusal(provider string, ue *upstreamError) bool {
	switch provider {
	case "opencode", "opencode-zen", "opencode-go":
	default:
		return false
	}
	if ue == nil {
		return false
	}
	// Match the error type, not the prose: the gate's message text has already
	// been reworded once.
	switch ue.StatusCode {
	case http.StatusForbidden:
		return bytes.Contains(ue.Body, []byte("FreeTierError"))
	case http.StatusTooManyRequests:
		return bytes.Contains(ue.Body, []byte("FreeUsageLimitError"))
	}
	return false
}

func isModelScopedError(statusCode int, errorText string, model string, bodies ...[]byte) bool {
	if model == "" {
		return false
	}
	lower := strings.ToLower(errorText)
	if isAccountAuthFailure(lower) {
		return false
	}
	// An OpenCode free-tier refusal is an egress verdict, not a model gate:
	// the failover loop answers it by rotating the proxy pool, so it must not
	// be recorded as a model-scoped lock. Matched on the body's error type —
	// the prose is not stable enough to key on, and every other 403 keeps the
	// existing cooldown-only handling (it never rotates a pool). The 429 shape
	// (FreeUsageLimitError) is caught by isOpencodeFreeTierRefusal before this
	// classifier runs; the 403 shape is kept here so a caller that reaches this
	// function directly still never treats the refusal as a model lock.
	if statusCode == http.StatusForbidden {
		for _, body := range bodies {
			if bytes.Contains(body, []byte("FreeTierError")) {
				return false
			}
		}
	}

	// 1. Quota exhaustion markers (Antigravity Claude, etc.) on 429/403/503
	if statusCode == http.StatusTooManyRequests || statusCode == http.StatusForbidden || statusCode == http.StatusServiceUnavailable {
		if isModelQuotaText(errorText) {
			return true
		}
	}

	// 2. Model-gate verdicts only ever arrive on 400/401/402/403.
	// A 5xx that merely says "not supported" is a node fault, not a model gate.
	switch statusCode {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden:
	default:
		return false
	}

	// 402 Insufficient account funds on providers serving tiered models (e.g. Zen paid models)
	if statusCode == http.StatusPaymentRequired {
		if (strings.Contains(lower, "insufficient account funds") || strings.Contains(lower, "insufficient funds")) &&
			!strings.Contains(lower, "credits are exhausted") && !strings.Contains(lower, "spending limit") {
			return true
		}
	}

	// 401/400/403: Body must name the requested model AND mention that the model is unsupported/disabled
	cleanModel := strings.ToLower(model)
	if idx := strings.LastIndex(cleanModel, "/"); idx != -1 {
		cleanModel = cleanModel[idx+1:]
	}
	if (strings.Contains(lower, cleanModel) || strings.Contains(lower, strings.ToLower(model))) && mentionsGate(lower) {
		return true
	}

	return false
}

func sanitizeOutboundToolSchemas(body []byte, provider string) []byte {
	var bodyMap map[string]any
	if err := json.Unmarshal(body, &bodyMap); err != nil {
		return body
	}
	tools, ok := bodyMap["tools"].([]any)
	if !ok || len(tools) == 0 {
		return body
	}
	normalized, removed := translator.NormalizeToolSchemasForProvider(provider, tools)
	if removed == 0 {
		return body
	}
	bodyMap["tools"] = normalized
	log.Debug("tool_schema", "stripped invalid patterns", "provider", provider, "removed", removed)
	if newBody, err := json.Marshal(bodyMap); err == nil {
		return newBody
	}
	return body
}

// credentialRejected reports a refusal of the credential itself — the only
// class a forced OAuth refresh can answer.
//
// A 401 always is one. A 403 is not: an unentitled, geo-blocked or quota-refused
// account is refused the same way, and spending the single-use refresh token on
// that would park an otherwise healthy account. So a 403 counts only when its
// body names the token. Kiro answers a dead OAuth access token with
//
//	403 {"message":"The bearer token included in the request is invalid."}
//
// instead of 401, which is why the reactive refresh never fired for it and the
// account stayed unusable — 403 on every turn — until someone logged in again
// by hand.
func credentialRejected(ue *upstreamError) bool {
	if ue == nil {
		return false
	}
	switch ue.StatusCode {
	case http.StatusUnauthorized:
		return true
	case http.StatusForbidden:
		body := strings.ToLower(string(ue.Body))
		for _, marker := range []string{
			"bearer token", "access token", "token is invalid", "invalid token",
			"token has expired", "token expired", "expired token",
		} {
			if strings.Contains(body, marker) {
				return true
			}
		}
	}
	return false
}

// bodySpeaksOpenAIChat reports whether this request body is in Chat
// Completions shape rather than the Claude Messages shape a /v1/messages
// client sends. A combo converts a client's body once, for its first member,
// so by the time a Claude member is tried the wire can be OpenAI's even though
// the provider is Anthropic — and the Messages API then answers 400 "Input tag
// 'function' found using 'type'".
