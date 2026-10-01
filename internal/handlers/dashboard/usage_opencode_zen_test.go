package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/models"
)

func withOpenCodeZenUsageServer(t *testing.T, status int, body string) *string {
	t.Helper()
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	prev := openCodeZenUsageURL
	openCodeZenUsageURL = srv.URL + "/zen/v1/usage"
	t.Cleanup(func() { openCodeZenUsageURL = prev })
	return &auth
}

func TestFetchOpenCodeZenUsage_MapsPeriods(t *testing.T) {
	auth := withOpenCodeZenUsageServer(t, http.StatusOK,
		`{"usage":{"rolling":{"percent":12.5,"resetsAt":"2026-10-02T00:00:00Z"},
		          "weekly":{"percent":"40","resetsAt":1788000000},
		          "monthly":{"percent":7}}}`)

	res := fetchOpenCodeZenUsage(context.Background(), "sk-zen", "")
	if len(res.quotas) != 3 {
		t.Fatalf("expected 3 quotas, got %d (%+v)", len(res.quotas), res.quotas)
	}
	if res.plan != "OpenCode Zen" {
		t.Errorf("plan = %q", res.plan)
	}
	if *auth != "Bearer sk-zen" {
		t.Errorf("expected Bearer auth, got %q", *auth)
	}
	rolling, _ := res.quotas["Rolling"].(map[string]any)
	if rolling["used"] != 12.5 || rolling["remaining"] != 87.5 || rolling["total"] != 100 {
		t.Errorf("rolling quota mis-mapped: %+v", rolling)
	}
	if rolling["resetAt"] != "2026-10-02T00:00:00Z" {
		t.Errorf("resetAt = %v", rolling["resetAt"])
	}
	weekly, _ := res.quotas["Weekly"].(map[string]any)
	if weekly["used"] != 40.0 {
		t.Errorf("a string percent must parse: %+v", weekly)
	}
	if _, ok := res.quotas["Monthly"]; !ok {
		t.Errorf("monthly quota missing: %+v", res.quotas)
	}
}

func TestFetchOpenCodeZenUsage_ClampsAndReports(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantMsg string
	}{
		{"no key", http.StatusOK, `{}`, "API key not available"},
		{"unauthorized", http.StatusUnauthorized, `{}`, "authentication failed"},
		{"entitlement", http.StatusForbidden, `{"error":{"type":"EntitlementError"}}`, "billing required"},
		{"forbidden", http.StatusForbidden, `{"error":{"type":"Nope"}}`, "access forbidden"},
		{"server error", http.StatusBadGateway, `{}`, "usage API error (502)"},
		{"no usage block", http.StatusOK, `{"ok":true}`, "did not contain quota data"},
		{"no usable periods", http.StatusOK, `{"usage":{"rolling":{}}}`, "valid quota data"},
		{"out of range percent", http.StatusOK, `{"usage":{"rolling":{"percent":150}}}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withOpenCodeZenUsageServer(t, tt.status, tt.body)
			key := "sk-zen"
			if tt.name == "no key" {
				key = "  "
			}
			res := fetchOpenCodeZenUsage(context.Background(), key, "")
			if tt.wantMsg == "" {
				rolling, _ := res.quotas["Rolling"].(map[string]any)
				if rolling["used"] != 100.0 {
					t.Errorf("percent above 100 must clamp, got %+v", rolling)
				}
				return
			}
			if !strings.Contains(res.message, tt.wantMsg) {
				t.Errorf("message = %q, want it to contain %q", res.message, tt.wantMsg)
			}
		})
	}
}

func TestOpenCodeZenIsUsageEligibleConnection(t *testing.T) {
	conn := &models.ProviderConnection{Provider: "opencode-zen", AuthType: "apikey"}
	if !isUsageEligibleConnection(conn) {
		t.Error("a keyed opencode-zen connection must appear in the quota tracker")
	}
}
