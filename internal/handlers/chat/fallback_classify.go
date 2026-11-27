package chat

import (
	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// opencodeClosureError turns a free-tier refusal that survived the executor's
// retries into a response the caller can act on, instead of forwarding the
// upstream body verbatim.
//
// The gate refuses in two shapes (see isOpencodeFreeTierRefusal): a 403
// FreeTierError when it is not serving free traffic from this egress at that
// moment, and a 429 FreeUsageLimitError once the free budget is spent. That is
// a temporary unavailability of a shared free tier, not a verdict on the
// caller's credentials — but a 403 is read as "you are not allowed" by every
// client, including this gateway's own callers, so an agent loop stops
// mid-tool-call and the user is left with a wall of text about downloading
// OpenCode.
//
// The classified answer keeps the caller's back-off honest: 429/503 plus a
// Retry-After, so a client waits and retries rather than giving up or assuming
// its key is wrong. The upstream's own wording is preserved in the message so
// the cause is still diagnosable, and the type names the condition so a client
// can branch on it without string matching.
type opencodeRefusalDetail struct {
	StatusCode int
	ErrorType  string
	Message    string
	RetryAfter int
}

func classifyOpencodeFreeTierRefusal(provider string, body []byte) opencodeRefusalDetail {
	msg := extractErrorText(body)
	lower := strings.ToLower(msg)
	// The type token is the stable half of the payload. extractErrorText keeps
	// only the message, and the usage-limit message ("Free usage limit reached.
	// Subscribe to OpenCode Go to keep going") names neither a rate limit nor a
	// quota — so the token has to be read for the shape to be classified.
	token := strings.ToLower(string(body))

	switch {
	case strings.Contains(lower, "not available in your country") ||
		strings.Contains(lower, "country_blocked") ||
		strings.Contains(lower, "country not allowed") ||
		strings.Contains(lower, "region"):
		return opencodeRefusalDetail{
			StatusCode: http.StatusForbidden,
			ErrorType:  "free_tier_region_blocked",
			Message:    fmt.Sprintf("OpenCode's free tier is not available in your region (%s). Route through a proxy pool in a supported country.", provider),
		}

	case isOpencodeQuotaRefusal(lower, token):
		return opencodeRefusalDetail{
			StatusCode: http.StatusTooManyRequests,
			ErrorType:  "free_tier_quota_exhausted",
			Message:    fmt.Sprintf("OpenCode free tier rate limit or quota exceeded for this egress. Retry in about 60s, rotate proxy pool, or use a paid %s key.", provider),
			RetryAfter: 60,
		}

	case strings.Contains(lower, "upgrade required") ||
		strings.Contains(lower, "upgrade_required") ||
		strings.Contains(lower, "client version") ||
		strings.Contains(lower, "version deprecated"):
		return opencodeRefusalDetail{
			StatusCode: http.StatusUpgradeRequired,
			ErrorType:  "free_tier_upgrade_required",
			Message:    "OpenCode client version is deprecated or requires upgrade.",
		}

	case strings.Contains(lower, "detect abuse traffic") ||
		strings.Contains(lower, "abuse") ||
		// The gate also words the same verdict without the word "abuse": "free
		// models aren't available ... from your current network". Both halves
		// are required — each appears alone in benign prose — so the pair is
		// matched conjunctively rather than by either phrase.
		(strings.Contains(lower, "free models aren't available") &&
			strings.Contains(lower, "from your current network")):
		return opencodeRefusalDetail{
			StatusCode: http.StatusServiceUnavailable,
			ErrorType:  "free_tier_abuse_detected",
			Message:    "OpenCode free tier gateway flagged abuse traffic on this egress. Route through a clean residential/ISP proxy pool, or retry in about 30s.",
			RetryAfter: 30,
		}

	default:
		const retryAfterSeconds = 30
		return opencodeRefusalDetail{
			StatusCode: http.StatusServiceUnavailable,
			ErrorType:  "free_tier_unavailable",
			Message: fmt.Sprintf(
				"OpenCode's free tier is not serving requests right now. This is an upstream condition rather than a problem with your request or credentials. Retry in about %ds, or use a paid %s key or another provider.",
				retryAfterSeconds, provider,
			),
			RetryAfter: retryAfterSeconds,
		}
	}
}

// isOpencodeQuotaRefusal reports whether the gate's answer is its usage-limit
// shape: the FreeUsageLimitError type token, or prose naming a rate limit or a
// quota (the 403 lane reports the same condition in words).
func isOpencodeQuotaRefusal(msg, token string) bool {
	return strings.Contains(token, "freeusagelimiterror") ||
		strings.Contains(msg, "rate limit") ||
		strings.Contains(msg, "too many requests") ||
		strings.Contains(msg, "quota") ||
		strings.Contains(msg, "usage limit")
}

func opencodeClosureError(provider, model string, err error) error {
	var ue *upstreamError
	if !errors.As(err, &ue) || !isOpencodeFreeTierRefusal(provider, ue) {
		return err
	}
	detail := classifyOpencodeFreeTierRefusal(provider, ue.Body)
	// The gate announces a spent allowance's real reset in Retry-After. The
	// classifier can only guess, so a carrier beats the guess: overwriting it
	// with the hardcoded minute made the caller re-probe a spent egress long
	// before the window it was told to wait for. The 2h maxResetCooldown cap
	// still applies — this changes the source of the value, not the ceiling.
	if real, ok := upstreamRetryAfterSec(ue); ok {
		detail.RetryAfter = real
	}
	body, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": detail.Message,
			"type":    detail.ErrorType,
			"code":    detail.StatusCode,
		},
	})
	log.Warn("fallback", "opencode free tier refused after retries; reporting classified failure",
		"provider", provider, "model", model, "type", detail.ErrorType, "status", detail.StatusCode,
		"retry_after_s", detail.RetryAfter)
	header := http.Header{
		"Content-Type": []string{"application/json"},
	}
	if detail.RetryAfter > 0 {
		header.Set("Retry-After", fmt.Sprint(detail.RetryAfter))
	}
	return &upstreamError{
		StatusCode: detail.StatusCode,
		Body:       body,
		Header:     header,
	}
}

// upstreamRetryAfterSec reads the wait the upstream named, clamped like every
// other announced cooldown. It reads the Retry-After header plus the body
// carriers retryAfterWait already knows, so a gate that announces the window in
// prose is honoured too. A missing or unparseable carrier reports false and the
// caller keeps the classifier's own estimate.
func upstreamRetryAfterSec(ue *upstreamError) (int, bool) {
	wait, ok := retryAfterWait(ue)
	if !ok || wait <= 0 {
		return 0, false
	}
	return ceilSeconds(min(wait, maxCooldownFor("opencode"))), true
}

// tryForwardWithConnection attempts a single upstream request using the given connection data.
// isAnthropicUpstream reports whether the request is headed to Anthropic's
// native Messages API (as opposed to an anthropic-compatible custom node).
func isAnthropicUpstream(provider string, cfg *providers.ProviderConfig) bool {
	if cfg == nil {
		return false
	}
	if cfg.UpstreamIsAnthropic {
		return true
	}
	if provider != "claude" && provider != "anthropic" {
		return false
	}
	if cfg == nil {
		return false
	}
	targetURL := cfg.BaseURL
	// Backward compatibility: if an unmigrated test or caller still supplies
	// the old header set, resolve the destination from it.
	if cfg.StaticHeaders != nil {
		if relayTarget, ok := cfg.StaticHeaders["x-relay-target"]; ok && relayTarget != "" {
			targetURL = relayTarget + cfg.StaticHeaders["x-relay-path"]
		}
	}
	return targetURL == "https://api.anthropic.com/v1/messages" ||
		strings.HasPrefix(targetURL, "https://api.anthropic.com/v1/messages?")
}

// servesClaudeMessages reports whether the provider's own endpoint speaks
// Anthropic Messages, so the body reaches it in Claude format and the reply
// comes back Claude-shaped.
//
// That is a different question from isAnthropicUpstream: Anthropic's own API
// additionally needs the beta query, the OAuth cloaking and the beta-flag
// merge below, none of which a third-party Messages endpoint wants. A provider
// declares the wire format on its registry entry (Format: "claude"), which is
// the same field the /v1/models metadata publishes.
func servesClaudeMessages(provider string, cfg *providers.ProviderConfig) bool {
	return cfg != nil && cfg.Format == providers.FormatClaude
}

func appendBetaQuery(u string) string {
	if strings.Contains(u, "beta=true") {
		return u
	}
	if strings.Contains(u, "?") {
		return u + "&beta=true"
	}
	return u + "?beta=true"
}
