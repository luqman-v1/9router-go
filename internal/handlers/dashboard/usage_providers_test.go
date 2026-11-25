package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/models"
)

func testUsageConn(provider, authType string) *models.ProviderConnection {
	return &models.ProviderConnection{ID: "t", Provider: provider, AuthType: authType}
}

func TestUsageResetTime(t *testing.T) {
	if got := usageResetTime(nil); got != "" {
		t.Errorf("nil: got %q", got)
	}
	// Unix seconds → RFC3339.
	if got := usageResetTime(float64(1700000000)); !strings.HasPrefix(got, "2023-11-14") {
		t.Errorf("seconds: got %q", got)
	}
	// Unix millis.
	if got := usageResetTime(float64(1700000000000)); !strings.HasPrefix(got, "2023-11-14") {
		t.Errorf("millis: got %q", got)
	}
	// Numeric string.
	if got := usageResetTime("1700000000"); !strings.HasPrefix(got, "2023-11-14") {
		t.Errorf("numeric string: got %q", got)
	}
	// ISO passthrough.
	if got := usageResetTime("2026-01-02T03:04:05Z"); got != "2026-01-02T03:04:05Z" {
		t.Errorf("iso: got %q", got)
	}
	if got := usageResetTime("not-a-date"); got != "" {
		t.Errorf("garbage: got %q", got)
	}
}

func TestMaskConnectionName(t *testing.T) {
	short := "Work account"
	if got := maskConnectionName(short); got != short {
		t.Errorf("short names untouched, got %q", got)
	}
	long := "abcdefghijklmnopqrstuvwxyz0123456789ABCD"
	if got := maskConnectionName(long); got != "abcdefgh***" {
		t.Errorf("token-like masked, got %q", got)
	}
	// Long but not token-like stays.
	human := "This is a fairly long connection name"
	if got := maskConnectionName(human); got != human {
		t.Errorf("human name untouched, got %q", got)
	}
}

func TestParseGroqDurationMs(t *testing.T) {
	ms, ok := parseGroqDurationMs("2m59.56s")
	if !ok || ms < 179000 || ms > 180000 {
		t.Errorf("2m59.56s: got %d,%v", ms, ok)
	}
	if _, ok := parseGroqDurationMs(""); ok {
		t.Errorf("empty must not parse")
	}
	if _, ok := parseGroqDurationMs("tomorrow"); ok {
		t.Errorf("garbage must not parse")
	}
}

func TestGrokMakeQuota(t *testing.T) {
	q := grokMakeQuota(30, 100, "")
	if q["used"] != 30.0 || q["total"] != 100.0 || q["remainingPercentage"] != 70.0 {
		t.Errorf("unexpected quota: %v", q)
	}
	q = grokMakeQuota(5, 0, "")
	if q["total"] != 0 || q["unlimited"] != true {
		t.Errorf("zero total must be unlimited row: %v", q)
	}
}

func TestUsageQuotaShape(t *testing.T) {
	q := usageQuota(25, 100, "2026-01-01T00:00:00Z")
	if q["remainingPercentage"] != 75.0 || q["resetAt"] != "2026-01-01T00:00:00Z" || q["unlimited"] != false {
		t.Errorf("unexpected: %v", q)
	}
}

func TestCodebuddyIsRefill(t *testing.T) {
	// DeductionEndTime is unix ms; gap >2d vs cycle end = refill.
	refill := map[string]any{
		"CycleEndTime":     "2026-09-30 23:59:59",
		"DeductionEndTime": float64(2049542858000),
	}
	if !codebuddyIsRefill(refill) {
		t.Errorf("far-expiry pack must be refill")
	}
	bonus := map[string]any{
		"CycleEndTime":     "2026-09-26 21:27:37",
		"DeductionEndTime": float64(1790429257000),
	}
	if codebuddyIsRefill(bonus) {
		t.Errorf("same-day-expiry pack must be bonus")
	}
}

func TestUsageResetTimeNaiveLocal(t *testing.T) {
	// Naive datetimes parse in server-local zone (mirrors JS new Date(str)).
	got := usageResetTime("2026-09-30 23:59:59")
	want := time.Date(2026, 9, 30, 23, 59, 59, 0, time.Local).UTC().Format(time.RFC3339)
	if got != want {
		t.Errorf("naive local: got %q want %q", got, want)
	}
}

func TestIsUsageEligibleConnection(t *testing.T) {
	cases := []struct {
		provider, authType string
		want               bool
	}{
		{"codex", "oauth", true},
		{"kimi", "apikey", true},
		{"kimi", "oauth", true},
		{"openai", "apikey", false},
		{"deepseek", "apikey", true},
		{"comfyui", "apikey", false},
		{"minimax-code", "mcode-lease", true},
		{"minimax-code-global", "mcode-lease", true},
		{"minimax-code", "oauth", true},
		{"minimax-code-global", "apikey", false},
	}
	for _, c := range cases {
		conn := testUsageConn(c.provider, c.authType)
		if got := isUsageEligibleConnection(conn); got != c.want {
			t.Errorf("%s/%s: got %v want %v", c.provider, c.authType, got, c.want)
		}
	}
}

// Go's transport announces "Go-http-client/1.1", which the WAFs in front of
// chatgpt.com, api.groq.com, api.deepseek.com, openapi.qoder.sh and
// www.codebuddy.ai answer with a bare 403. That surfaced as an opaque
// `Get "…": Forbidden` transport error on the whole quota tracker, so every
// fetcher now announces a real User-Agent unless it sets its own.
func TestUsageDo_SendsNonGoUserAgent(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{
			name:    "defaults to the gateway UA",
			headers: map[string]string{"Authorization": "Bearer k"},
			want:    usageUserAgent,
		},
		{
			name:    "a fetcher's own UA is preserved",
			headers: map[string]string{"User-Agent": "grok_cli_rs/1.2.3"},
			want:    "grok_cli_rs/1.2.3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotUA string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotUA = r.Header.Get("User-Agent")
				_, _ = w.Write([]byte(`{}`))
			}))
			defer srv.Close()

			if _, _, _, err := usageGet(t.Context(), srv.URL, tt.headers); err != nil {
				t.Fatalf("usageGet() error = %v", err)
			}
			if gotUA != tt.want {
				t.Errorf("User-Agent = %q, want %q", gotUA, tt.want)
			}
			if strings.Contains(gotUA, "Go-http-client") {
				t.Errorf("User-Agent = %q must not be the Go default", gotUA)
			}
		})
	}
}
