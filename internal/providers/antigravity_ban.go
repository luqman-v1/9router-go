// Package providers: antigravity ToS ban detection.
//
// Google permanently disables Antigravity (Gemini Code Assist / Cloud Code)
// OAuth accounts for Terms-of-Service violations, answering HTTP 403 with a
// body whose error.details[].reason is "TOS_VIOLATION" and whose details
// carry metadata.appeal_url -- a Google forms appeal link. That link contains
// a continuation token and must never be written to the DB or logs in the
// clear: anyone holding it can resume the banned account's appeal flow, so
// ExtractAppealURL validates hosts and RedactAppealURLs masks every allowed
// link before text is stored or logged.
//
// Pure helpers: no DB, no logging, no HTTP.
package providers

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// allowedAppealHosts are the only hosts Google's appeal link may live on.
// Anything else is rejected upstream, so a hostile 403 body cannot inject an
// arbitrary link into the DB or a log line.
var allowedAppealHosts = map[string]bool{
	"forms.gle":           true,
	"accounts.google.com": true,
	"docs.google.com":     true,
}

// appealURLRegexp finds appeal URLs in free text. It is case-insensitive
// ((?i)) and accepts JSON-escaped slashes (\/) as well as plain slashes. The
// tail stops at a backslash that is not an escaped slash, so surrounding text
// such as `https://accounts.google.com/c?x=1\"` is not swallowed.
var appealURLRegexp = regexp.MustCompile(`(?i)https:(?:\\/\\/|//)(?:accounts\.google\.com|forms\.gle|docs\.google\.com(?:/|\\/)forms)(?:/|\\/)(?:[^\s"'\\<>]|\\/)*`)

// redactURLRegexp masks appeal URLs in text meant for storage or logging. It is
// deliberately looser than appealURLRegexp: it runs past a backslash, because a
// raw JSON body can hide a token behind &. Over-masking a log is the safe
// side. Each match is replaced with a constant-size token, so untrusted text
// cannot blow up the output; the allowlist in IsGoogleAppealURL is the security
// boundary.
var redactURLRegexp = regexp.MustCompile(`(?i)https:(?:\\/\\/|//)(?:accounts\.google\.com|forms\.gle|docs\.google\.com(?:/|\\/)forms)(?:/|\\/)[^\s"<>]*`)

// IsAntigravityTOSViolation reports whether an upstream answer is Google's
// permanent Antigravity TOS ban: status 403 and a body that either names
// reason TOS_VIOLATION, pairs cloudcode-pa.googleapis.com with
// PERMISSION_DENIED, or contains the ban sentence. Never fails on malformed
// input: returns false.
func IsAntigravityTOSViolation(status int, body []byte) bool {
	if status != http.StatusForbidden {
		return false
	}
	lower := strings.ToLower(string(body))
	if strings.Contains(lower, "disabled in this account for violation") {
		return true
	}
	var parsed struct {
		Error struct {
			Status  string `json:"status"`
			Details []struct {
				Reason string `json:"reason"`
				Domain string `json:"domain"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return false
	}
	for _, detail := range parsed.Error.Details {
		if detail.Reason == "TOS_VIOLATION" {
			return true
		}
		if detail.Domain == "cloudcode-pa.googleapis.com" && parsed.Error.Status == "PERMISSION_DENIED" {
			return true
		}
	}
	return false
}

// ExtractAppealURL returns the appeal link Google shipped in the error body.
// Candidates are checked in order:
// error.details[].metadata.appeal_url, appealUrl, validation_url,
// validationUrl, then error.appeal_url and the top-level appeal_url.
// Each candidate is run through the free-text extractor so surrounding text
// is trimmed, then normalized (\/ -> /) and validated with IsGoogleAppealURL.
// ok is false when the body carries none, or no candidate passes validation.
// The returned URL is always plain https://... .
func ExtractAppealURL(body []byte) (url string, ok bool) {
	var parsed struct {
		Error struct {
			AppealURL string `json:"appeal_url"`
			Details   []struct {
				Metadata map[string]any `json:"metadata"`
			} `json:"details"`
		} `json:"error"`
		AppealURL string `json:"appeal_url"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", false
	}
	candidates := make([]string, 0, 4*len(parsed.Error.Details)+2)
	for _, detail := range parsed.Error.Details {
		if detail.Metadata == nil {
			continue
		}
		for _, key := range []string{"appeal_url", "appealUrl", "validation_url", "validationUrl"} {
			if v, ok := detail.Metadata[key].(string); ok && v != "" {
				candidates = append(candidates, v)
			}
		}
	}
	candidates = append(candidates, parsed.Error.AppealURL, parsed.AppealURL)
	for _, raw := range candidates {
		extracted := appealURLRegexp.FindString(raw)
		if extracted == "" {
			continue
		}
		normalized := strings.ReplaceAll(extracted, "\\/", "/")
		if IsGoogleAppealURL(normalized) {
			return normalized, true
		}
	}
	return "", false
}

// IsGoogleAppealURL reports whether raw is an https URL on an allowed appeal
// host. docs.google.com is further restricted to /forms/ paths so a Google
// document link cannot pass as an appeal URL. JSON-escaped slashes (\/) are
// normalized before validation.
func IsGoogleAppealURL(raw string) bool {
	normalized := strings.ReplaceAll(raw, "\\/", "/")
	u, err := url.Parse(normalized)
	if err != nil {
		return false
	}
	if strings.ToLower(u.Scheme) != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if !allowedAppealHosts[host] {
		return false
	}
	if host == "docs.google.com" {
		return strings.HasPrefix(u.Path, "/forms/")
	}
	return true
}

// LooksLikeAccountDeactivated reports whether an error text names a permanent
// account deactivation (ban), not a transient auth failure.
// Case-insensitive substring match. tos_violation only counts when scoped to
// Google Cloud Code (cloudcode-pa): a bare tos_violation (another provider's
// refusal, or a user prompt echoed into an error) must not park a healthy
// connection.
func LooksLikeAccountDeactivated(text string) bool {
	lower := strings.ToLower(text)
	for _, p := range []string{
		"account_deactivated",
		"account has been deactivated",
		"account has been disabled",
		"your account has been suspended",
		"this service has been disabled in this account for violation",
		"this service has been disabled in this account",
	} {
		if strings.Contains(lower, p) {
			return true
		}
	}
	if strings.Contains(lower, "tos_violation") && strings.Contains(lower, "cloudcode-pa") {
		return true
	}
	return false
}

// RedactAppealURLs masks every Google appeal link in text with
// [verification-url] so a body can be stored or logged safely.
func RedactAppealURLs(text string) string {
	return redactURLRegexp.ReplaceAllStringFunc(text, func(string) string {
		return "[verification-url]"
	})
}

// DefaultProtectivePauseDuration is the default pool-wide protective pause
// length: once Google sweeps the Antigravity pool with a TOS_VIOLATION ban,
// stop routing to Antigravity for this window so the remaining accounts are
// not burned one after another.
const DefaultProtectivePauseDuration = 6 * time.Hour

var (
	protectivePauseMu     sync.RWMutex
	protectivePauseActive bool
	protectivePauseUntil  time.Time
)

// TriggerProtectivePause activates the pool-wide protective pause for the
// given duration. A subsequent call while a pause is active extends the
// deadline only if the new deadline is later than the current one (the pause
// is never shortened by a later trigger).
func TriggerProtectivePause(duration time.Duration) {
	if duration <= 0 {
		duration = DefaultProtectivePauseDuration
	}
	protectivePauseMu.Lock()
	defer protectivePauseMu.Unlock()
	until := time.Now().UTC().Add(duration)
	if !protectivePauseActive || until.After(protectivePauseUntil) {
		protectivePauseActive = true
		protectivePauseUntil = until
	}
}

// IsProtectivePauseActive reports whether the pool-wide protective pause is
// currently active and how long remains on it. Returns false, 0 when no pause
// is in effect.
func IsProtectivePauseActive() (bool, time.Duration) {
	protectivePauseMu.RLock()
	defer protectivePauseMu.RUnlock()
	if !protectivePauseActive {
		return false, 0
	}
	if now := time.Now().UTC(); now.After(protectivePauseUntil) {
		return false, 0
	} else {
		return true, protectivePauseUntil.Sub(now)
	}
}

// ResetProtectivePause clears the protective pause state immediately, allowing
// Antigravity routing to resume. Intended for tests and operator reset.
func ResetProtectivePause() {
	protectivePauseMu.Lock()
	defer protectivePauseMu.Unlock()
	protectivePauseActive = false
	protectivePauseUntil = time.Time{}
}
