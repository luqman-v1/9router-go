package providers

import (
	"net/http"
	"strings"
	"testing"
)

const tosBanBody = `{
  "error": {
    "code": 403,
    "message": "This service has been disabled in this account for violation of Terms of Service. Please submit an appeal to continue using this product.",
    "status": "PERMISSION_DENIED",
    "details": [
      {
        "@type": "type.googleapis.com/google.rpc.ErrorInfo",
        "reason": "TOS_VIOLATION",
        "domain": "cloudcode-pa.googleapis.com",
        "metadata": {
          "uiMessage": "true",
          "appeal_url": "https://forms.gle/hGzM9MEUv2azZsrb9",
          "appeal_url_link_text": "Submit Appeal"
        }
      }
    ]
  }
}`

func TestIsAntigravityTOSViolation(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{name: "exact google ban body", status: http.StatusForbidden, body: tosBanBody, want: true},
		{name: "TOS_VIOLATION reason without metadata", status: http.StatusForbidden, body: `{"error":{"code":403,"status":"PERMISSION_DENIED","details":[{"reason":"TOS_VIOLATION"}]}}`, want: true},
		{name: "cloudcode domain plus PERMISSION_DENIED", status: http.StatusForbidden, body: `{"error":{"status":"PERMISSION_DENIED","details":[{"domain":"cloudcode-pa.googleapis.com"}]}}`, want: true},
		{name: "ban sentence anywhere in body", status: http.StatusForbidden, body: `{"error":{"message":"this service has been disabled in this account for violation"}}`, want: true},
		{name: "unrelated 403", status: http.StatusForbidden, body: `{"error":{"message":"Access denied"}}`, want: false},
		{name: "ban body with 200 status", status: http.StatusOK, body: tosBanBody, want: false},
		{name: "malformed json", status: http.StatusForbidden, body: `not json`, want: false},
		{name: "empty body", status: http.StatusForbidden, body: ``, want: false},
		{name: "non-403 with ban text", status: http.StatusTooManyRequests, body: tosBanBody, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsAntigravityTOSViolation(tt.status, []byte(tt.body)); got != tt.want {
				t.Errorf("IsAntigravityTOSViolation(%d, %q) = %v, want %v", tt.status, tt.body, got, tt.want)
			}
		})
	}
}

func TestExtractAppealURL(t *testing.T) {
	url, ok := ExtractAppealURL([]byte(tosBanBody))
	if !ok {
		t.Fatal("expected an appeal URL")
	}
	if url != "https://forms.gle/hGzM9MEUv2azZsrb9" {
		t.Errorf("appeal URL = %q, want %q", url, "https://forms.gle/hGzM9MEUv2azZsrb9")
	}

	// TOS_VIOLATION without metadata -> detection true, no appeal URL.
	if _, ok := ExtractAppealURL([]byte(`{"error":{"details":[{"reason":"TOS_VIOLATION"}]}}`)); ok {
		t.Error("expected no appeal URL without metadata")
	}

	// Hostile host is rejected even inside a 403 ban body.
	hostile := `{"error":{"status":"PERMISSION_DENIED","details":[{"domain":"cloudcode-pa.googleapis.com","metadata":{"appeal_url":"https://evil.example/x"}}]}}`
	if _, ok := ExtractAppealURL([]byte(hostile)); ok {
		t.Error("expected hostile appeal URL to be rejected")
	}

	// Other metadata spellings are tried in order.
	second := `{"error":{"details":[{"metadata":{"validationUrl":"https://forms.gle/second"}}]}}`
	if u, _ := ExtractAppealURL([]byte(second)); u != "https://forms.gle/second" {
		t.Errorf("appeal URL = %q", u)
	}

	// Top-level spellings.
	third := `{"appeal_url":"https://forms.gle/third"}`
	if u, _ := ExtractAppealURL([]byte(third)); u != "https://forms.gle/third" {
		t.Errorf("appeal URL = %q", u)
	}

	if _, ok := ExtractAppealURL([]byte(`{"error":{"message":"no link"}}`)); ok {
		t.Error("expected no appeal URL")
	}
	if _, ok := ExtractAppealURL([]byte(`broken`)); ok {
		t.Error("expected no appeal URL on malformed input")
	}

	// JSON-escaped slashes are normalized to plain https:// before validation.
	escaped := `{"error":{"details":[{"metadata":{"appeal_url":"https:\/\/forms.gle\/abc\/def"}}]}}`
	if u, _ := ExtractAppealURL([]byte(escaped)); u != "https://forms.gle/abc/def" {
		t.Errorf("escaped appeal URL = %q", u)
	}

	// Free-text candidate: backslash that is not an escaped slash stops extraction.
	stop := `{"error":{"details":[{"metadata":{"appeal_url":"verify at https://accounts.google.com/c?x=1\\extra then"}}]}}`
	if u, _ := ExtractAppealURL([]byte(stop)); u != "https://accounts.google.com/c?x=1" {
		t.Errorf("backslash-stop appeal URL = %q", u)
	}

	// Lookalike host is rejected; only the real accounts.google.com URL is extracted.
	lookalike := `{"error":{"details":[{"metadata":{"appeal_url":"https://accounts.google.com.evil.com/x or https://accounts.google.com/real"}}]}}`
	if u, _ := ExtractAppealURL([]byte(lookalike)); u != "https://accounts.google.com/real" {
		t.Errorf("lookalike appeal URL = %q", u)
	}
}

func TestIsGoogleAppealURL(t *testing.T) {
	tests := []struct {
		raw  string
		want bool
	}{
		{"https://forms.gle/hGzM9MEUv2azZsrb9", true},
		{"https://docs.google.com/forms/d/e/FAKE/viewform", true},
		{"https://accounts.google.com/o/oauth2/auth", true},
		// Uppercase scheme and host are accepted after normalization.
		{"HTTPS://FORMS.GLE/XYZ", true},
		// JSON-escaped slashes are normalized before validation.
		{`https:\/\/forms.gle\/abc`, true},
		{"https://docs.google.com/document/d/FAKE", false},
		{"https://docs.google.com/spreadsheets/FAKE", false},
		{"https://evil.example/x", false},
		{"http://forms.gle/hGzM9MEUv2azZsrb9", false},
		{"not a url", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsGoogleAppealURL(tt.raw); got != tt.want {
			t.Errorf("IsGoogleAppealURL(%q) = %v, want %v", tt.raw, got, tt.want)
		}
	}
}

func TestLooksLikeAccountDeactivated(t *testing.T) {
	if !LooksLikeAccountDeactivated(tosBanBody) {
		t.Error("expected the ban body to read as deactivated")
	}
	if !LooksLikeAccountDeactivated("your account has been suspended") {
		t.Error("expected suspension wording to match")
	}
	if LooksLikeAccountDeactivated("invalid api key") {
		t.Error("expected transient auth failure to not match")
	}
	if LooksLikeAccountDeactivated("") {
		t.Error("expected empty text to not match")
	}

	// A bare tos_violation from a non-Google provider must not count.
	if LooksLikeAccountDeactivated(`{"error":{"code":"tos_violation","message":"Your request violates our Terms of Service"}}`) {
		t.Error("expected bare tos_violation without cloudcode-pa to not count")
	}
	// A prompt echoed into an error must not count either.
	if LooksLikeAccountDeactivated("Cannot summarise the tos_violation clause of the terms of service") {
		t.Error("expected prompt-echoed tos_violation to not count")
	}
	// The real Cloud Code body counts even when the message is in another
	// language, because tos_violation is scoped to cloudcode-pa.
	cloudcodeGerman := `{"error":{"code":403,"message":"Dieser Dienst wurde gesperrt.","status":"PERMISSION_DENIED","details":[{"reason":"TOS_VIOLATION","domain":"cloudcode-pa.googleapis.com"}]}}`
	if !LooksLikeAccountDeactivated(cloudcodeGerman) {
		t.Error("expected cloudcode-pa-scoped tos_violation to count even in another language")
	}
}

func TestRedactAppealURLs(t *testing.T) {
	tests := []struct {
		text string
		want string
	}{
		{tosBanBody, strings.Replace(tosBanBody, "https://forms.gle/hGzM9MEUv2azZsrb9", "[verification-url]", 1)},
		{"plain text, no links here", "plain text, no links here"},
		{"see https://evil.example/x and https://forms.gle/hGzM9MEUv2azZsrb9", "see https://evil.example/x and [verification-url]"},
		// Redaction runs past a backslash (raw JSON can hide a token behind &).
		{`{"url":"https://accounts.google.com/c?x=1&token=secret"}`, `{"url":"[verification-url]"}`},
		{"", ""},
	}
	for _, tt := range tests {
		if got := RedactAppealURLs(tt.text); got != tt.want {
			t.Errorf("RedactAppealURLs(%q) = %q, want %q", tt.text, got, tt.want)
		}
	}
}
