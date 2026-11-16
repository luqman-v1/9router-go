package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func withClinePassUsageServer(t *testing.T, status int, body string) (*string, *http.Header) {
	t.Helper()
	var auth string
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		hdr = r.Header.Clone()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	prev := clinePassUsageURL
	clinePassUsageURL = srv.URL
	t.Cleanup(func() { clinePassUsageURL = prev })
	return &auth, &hdr
}

func TestFetchClinePassUsage_MapsThreeWindows(t *testing.T) {
	auth, hdr := withClinePassUsageServer(t, http.StatusOK, `{
		"success": true,
		"data": {"limits": [
			{"type": "monthly", "percentUsed": 7, "resetsAt": "2026-12-01T00:00:00Z"},
			{"type": "five_hour", "percentUsed": 25, "resetsAt": 1788000000},
			{"type": "weekly", "percentUsed": "40.5", "resetsAt": "2026-11-20T00:00:00Z"},
			{"type": "unknown_window", "percentUsed": 99}
		]}
	}`)

	res := fetchClinePassUsage(context.Background(), "", "clp_live_key")
	if res.plan != "ClinePass" {
		t.Errorf("plan = %q, want ClinePass", res.plan)
	}
	if res.message != "" {
		t.Errorf("unexpected message %q", res.message)
	}
	if len(res.quotas) != 3 {
		t.Fatalf("expected 3 quotas, got %d (%+v)", len(res.quotas), res.quotas)
	}
	if *auth != "Bearer clp_live_key" {
		t.Errorf("API key must ride plain Bearer, got %q", *auth)
	}
	if got := hdr.Get("X-Client-Type"); got != "" {
		t.Errorf("an API key must not carry Cline identity headers, got X-Client-Type=%q", got)
	}

	five, ok := res.quotas["Session (5h)"].(map[string]any)
	if !ok {
		t.Fatalf("five_hour window missing: %+v", res.quotas)
	}
	if five["used"] != 25.0 || five["remaining"] != 75.0 || five["total"] != 100.0 {
		t.Errorf("five_hour mis-mapped: %+v", five)
	}
	if five["resetAt"] == nil {
		t.Errorf("five_hour resetAt dropped: %+v", five)
	}

	weekly, ok := res.quotas["Weekly (7d)"].(map[string]any)
	if !ok {
		t.Fatalf("weekly window missing: %+v", res.quotas)
	}
	if weekly["used"] != 40.5 {
		t.Errorf("a string percentUsed must parse: %+v", weekly)
	}
	if _, ok := res.quotas["Monthly"]; !ok {
		t.Errorf("monthly window missing: %+v", res.quotas)
	}
}

func TestFetchClinePassUsage_OAuthUsesWorkOSIdentity(t *testing.T) {
	auth, hdr := withClinePassUsageServer(t, http.StatusOK,
		`{"data":{"limits":[{"type":"five_hour","percentUsed":140}]}}`)
	jwt := "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiIxIn0.sig"

	res := fetchClinePassUsage(context.Background(), jwt, "")
	if len(res.quotas) != 1 {
		t.Fatalf("expected the single reported window, got %+v", res.quotas)
	}
	if *auth != "Bearer workos:"+jwt {
		t.Errorf("OAuth token must take the workos: prefix, got %q", *auth)
	}
	if hdr.Get("X-Client-Type") != "9router" {
		t.Errorf("OAuth reads must carry the Cline identity headers, got %q", hdr.Get("X-Client-Type"))
	}
	five, _ := res.quotas["Session (5h)"].(map[string]any)
	if five["used"] != 100.0 || five["remaining"] != 0.0 {
		t.Errorf("percentUsed must clamp to 0..100: %+v", five)
	}
}

func TestFetchClinePassUsage_Failures(t *testing.T) {
	t.Run("no credential", func(t *testing.T) {
		res := fetchClinePassUsage(context.Background(), " ", " ")
		if !strings.Contains(res.message, "not available") {
			t.Errorf("message = %q", res.message)
		}
	})

	t.Run("auth rejected", func(t *testing.T) {
		withClinePassUsageServer(t, http.StatusForbidden, `{"error":"nope"}`)
		res := fetchClinePassUsage(context.Background(), "", "clp_bad")
		if res.plan != "ClinePass" || !strings.Contains(res.message, "re-authorize") {
			t.Errorf("plan=%q message=%q", res.plan, res.message)
		}
	})

	t.Run("no limits reported", func(t *testing.T) {
		withClinePassUsageServer(t, http.StatusOK, `{"success":true,"data":{"limits":[]}}`)
		res := fetchClinePassUsage(context.Background(), "", "clp_ok")
		if res.plan != "ClinePass" || !strings.Contains(res.message, "No usage limits") {
			t.Errorf("plan=%q message=%q", res.plan, res.message)
		}
	})
}

func TestClinePassUsageEligibility(t *testing.T) {
	if !strSliceContains(usageSupportedProviders, "clinepass") {
		t.Error("clinepass missing from usageSupportedProviders")
	}
	if !strSliceContains(usageApikeyProviders, "clinepass") {
		t.Error("clinepass must be in usageApikeyProviders: its key reads the same endpoint")
	}
}
