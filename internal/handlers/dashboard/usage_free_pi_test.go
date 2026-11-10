package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// freePiStatsServer serves a chosen /me/stats body and records whether it saw a
// bearer credential.
type freePiStatsServer struct {
	srv        *httptest.Server
	sawBearer  bool
	statusCode int
	body       string
}

func newFreePiStatsServer(t *testing.T, status int, body string) *freePiStatsServer {
	t.Helper()
	f := &freePiStatsServer{statusCode: status, body: body}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/me/stats" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got == "Bearer free-pi-jwt" {
			f.sawBearer = true
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.statusCode)
		_, _ = w.Write([]byte(f.body))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func TestFreePiStatsURLFromBase(t *testing.T) {
	tests := []struct {
		base string
		want string
	}{
		{base: "https://api.freepi.ai/v1/chat/completions", want: "https://api.freepi.ai/me/stats"},
		{base: "https://api.freepi.ai/v1", want: "https://api.freepi.ai/me/stats"},
		{base: "https://api.freepi.ai/", want: "https://api.freepi.ai/me/stats"},
		{base: "http://127.0.0.1:8080/v1/chat/completions", want: "http://127.0.0.1:8080/me/stats"},
		// Empty must not produce a relative "/me/stats" that silently hits
		// whatever host the process happens to be talking to.
		{base: "", want: "https://api.freepi.ai/me/stats"},
	}
	for _, tt := range tests {
		t.Run(tt.base, func(t *testing.T) {
			if got := freePiStatsURLFromBase(tt.base); got != tt.want {
				t.Errorf("freePiStatsURLFromBase(%q) = %q, want %q", tt.base, got, tt.want)
			}
		})
	}
}

// TestFetchFreePiUsage_MapsCreditsAndTokens checks the field mapping against the
// shape recorded in testdata/freepi/me-stats.json. free-pi reports credits, not a
// dollar budget, so the daily quota must come out in credits and a used count
// derived from remaining-of-allowance rather than a spend figure.
func TestFetchFreePiUsage_MapsCreditsAndTokens(t *testing.T) {
	srv := newFreePiStatsServer(t, http.StatusOK, `{
		"tier": "free",
		"spent_usd_today": 0.0123,
		"cap_usd_today": 0.5,
		"prompt_tokens_today": 41234,
		"completion_tokens_today": 8192,
		"lifetime": {"prompt_tokens": 990234, "completion_tokens": 148920},
		"credit_credits": 120,
		"remaining_credits_today": 380,
		"allowance_credits": 400,
		"refill_at": "2026-11-10T00:00:00.000Z",
		"lane": "fallback"
	}`)

	res := fetchFreePiUsage(t.Context(), "free-pi-jwt", srv.srv.URL+"/v1/chat/completions")

	if !srv.sawBearer {
		t.Error("the request did not carry the connection credential")
	}
	if res.message != "" {
		t.Fatalf("unexpected message: %s", res.message)
	}
	if res.plan != "free" {
		t.Errorf("plan = %q, want free", res.plan)
	}

	daily, ok := res.quotas["daily"].(map[string]any)
	if !ok {
		t.Fatalf("daily quota missing: %v", res.quotas)
	}
	if daily["used"] != int64(20) {
		t.Errorf("daily used = %v, want 20 (400 allowance - 380 remaining)", daily["used"])
	}
	if daily["total"] != int64(400) {
		t.Errorf("daily total = %v, want 400", daily["total"])
	}
	if daily["unit"] != "credits" {
		t.Errorf("daily unit = %v, want credits", daily["unit"])
	}
	if daily["resetsAt"] != "2026-11-10T00:00:00.000Z" {
		t.Errorf("resetsAt = %v", daily["resetsAt"])
	}

	purchased, ok := res.quotas["purchased"].(map[string]any)
	if !ok {
		t.Fatalf("purchased credits missing: %v", res.quotas)
	}
	if purchased["remaining"] != int64(120) {
		t.Errorf("purchased remaining = %v, want 120", purchased["remaining"])
	}

	// The slow lane explains a latency change the user would otherwise blame on
	// the gateway, so it has to survive the mapping.
	if _, ok := res.quotas["lane"]; !ok {
		t.Error("fallback lane not surfaced")
	}

	tokens, ok := res.extra["tokensToday"].(map[string]any)
	if !ok {
		t.Fatalf("token counters missing: %v", res.extra)
	}
	if tokens["prompt"] != int64(41234) || tokens["completion"] != int64(8192) {
		t.Errorf("token counters = %v", tokens)
	}
}

// TestFetchFreePiUsage_ToleratesOlderServer: several fields were added over time,
// and an older server omits them. Absent must render as absent, not as a zero
// quota row claiming the user has nothing left.
func TestFetchFreePiUsage_ToleratesOlderServer(t *testing.T) {
	srv := newFreePiStatsServer(t, http.StatusOK, `{
		"tier": "free",
		"prompt_tokens_today": 10,
		"completion_tokens_today": 5
	}`)

	res := fetchFreePiUsage(t.Context(), "free-pi-jwt", srv.srv.URL)

	if res.message != "" {
		t.Fatalf("an older server must not read as an error: %s", res.message)
	}
	if len(res.quotas) != 0 {
		t.Errorf("quotas = %v, want none when the server reports no credit fields", res.quotas)
	}
	if res.plan != "free" {
		t.Errorf("plan = %q, want the free fallback", res.plan)
	}
	if _, ok := res.extra["lifetimeTokens"]; ok {
		t.Error("lifetime tokens were reported but the server sent none")
	}
}

func TestFetchFreePiUsage_ExpiredCredentialSaysSo(t *testing.T) {
	srv := newFreePiStatsServer(t, http.StatusUnauthorized, `{"error":"unauthorized"}`)

	res := fetchFreePiUsage(t.Context(), "free-pi-jwt", srv.srv.URL)

	if res.message == "" {
		t.Fatal("a 401 produced no message")
	}
	if !strings.Contains(res.message, "sign in again") {
		t.Errorf("message = %q, want it to name the only remedy (free-pi has no refresh token)", res.message)
	}
}

func TestFetchFreePiUsage_NoCredential(t *testing.T) {
	res := fetchFreePiUsage(t.Context(), "", "https://api.freepi.ai/v1/chat/completions")
	if res.message == "" {
		t.Error("a connection with no credential should explain itself")
	}
}

func TestFreePiIsUsageEligible(t *testing.T) {
	// The provider must appear in both lists, or /api/usage/providers filters it
	// out and the dashboard never offers the quota panel.
	if !strSliceContains(usageSupportedProviders, "free-pi") {
		t.Error("free-pi missing from usageSupportedProviders")
	}
	if !strSliceContains(usageApikeyProviders, "free-pi") {
		t.Error("free-pi missing from usageApikeyProviders")
	}
}
