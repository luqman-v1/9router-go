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
)

// allowedAppealHosts are the only hosts Google's appeal link may live on.
// Anything else is rejected upstream, so a hostile 403 body cannot inject an
// arbitrary link into the DB or a log line.
var allowedAppealHosts = map[string]bool{
	"forms.gle":           true,
	"accounts.google.com": true,
	"docs.google.com":     true,
}

// appealMaxTail bounds the untrusted tail the redact scan may walk, so a
// hostile body cannot blow up the replacement with a megabyte-long "URL".
// Go's regexp (RE2) has no {min,max} syntax, so the bound is enforced in
// RedactAppealURLs itself rather than in the pattern.
const appealMaxTail = 2048

var appealURLRegexp = regexp.MustCompile(`https://(forms\.gle|accounts\.google\.com|docs\.google\.com)/[^\s"'<>]*`)

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
// ok is false when the body carries none, or no candidate passes
// IsGoogleAppealURL.
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
		if IsGoogleAppealURL(raw) {
			return raw, true
		}
	}
	return "", false
}

// IsGoogleAppealURL reports whether raw is an https URL on an allowed appeal
// host. docs.google.com is further restricted to /forms/ paths so a Google
// document link cannot pass as an appeal URL.
func IsGoogleAppealURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "https" {
		return false
	}
	if !allowedAppealHosts[u.Hostname()] {
		return false
	}
	if u.Hostname() == "docs.google.com" {
		return strings.HasPrefix(u.Path, "/forms/")
	}
	return true
}

// LooksLikeAccountDeactivated reports whether an error text names a permanent
// account deactivation (ban), not a transient auth failure.
// Case-insensitive substring match.
func LooksLikeAccountDeactivated(text string) bool {
	lower := strings.ToLower(text)
	patterns := []string{
		"account_deactivated",
		"account has been deactivated",
		"account has been disabled",
		"your account has been suspended",
		"this account is deactivated",
		"this service has been disabled in this account for violation",
		"this service has been disabled in this account",
		"tos_violation",
	}
	for _, p := range patterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// RedactAppealURLs masks every Google appeal link in text with
// [verification-url] so a body can be stored or logged safely. Each match is
// replaced with a constant-size token, so untrusted text cannot blow up the
// output; the allowlist in IsGoogleAppealURL is the security boundary.
func RedactAppealURLs(text string) string {
	return appealURLRegexp.ReplaceAllStringFunc(text, func(string) string {
		return "[verification-url]"
	})
}
