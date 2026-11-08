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
}

func TestIsGoogleAppealURL(t *testing.T) {
	tests := []struct {
		raw  string
		want bool
	}{
		{"https://forms.gle/hGzM9MEUv2azZsrb9", true},
		{"https://docs.google.com/forms/d/e/FAKE/viewform", true},
		{"https://accounts.google.com/o/oauth2/auth", true},
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
}

func TestRedactAppealURLs(t *testing.T) {
	tests := []struct {
		text string
		want string
	}{
		{tosBanBody, strings.Replace(tosBanBody, "https://forms.gle/hGzM9MEUv2azZsrb9", "[verification-url]", 1)},
		{"plain text, no links here", "plain text, no links here"},
		{"see https://evil.example/x and https://forms.gle/hGzM9MEUv2azZsrb9", "see https://evil.example/x and [verification-url]"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := RedactAppealURLs(tt.text); got != tt.want {
			t.Errorf("RedactAppealURLs(%q) = %q, want %q", tt.text, got, tt.want)
		}
	}
}
