package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/providers"
)

func stubCommandCodeAPI(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	previous := commandCodeAPIBase
	commandCodeAPIBase = srv.URL
	t.Cleanup(func() { commandCodeAPIBase = previous })
	return srv
}

func TestFetchCommandCodeUsage_BarePlanIDResolves(t *testing.T) {
	stubCommandCodeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/alpha/whoami":
			w.Write([]byte(`{"org":{"id":"org-1"}}`))
		case "/alpha/billing/credits":
			w.Write([]byte(`{"credits":{"monthlyCredits":7,"purchasedCredits":0,"freeCredits":0},
				"windowLimits":{"fiveHour":{"used":2,"cap":10}}}`))
		case "/alpha/billing/subscriptions":
			w.Write([]byte(`{"data":{"planId":"go","currentPeriodEnd":"2026-11-01T00:00:00Z"}}`))
		default:
			http.NotFound(w, r)
		}
	})

	res := fetchCommandCodeUsage(context.Background(), "k")
	if res.plan != "Go" {
		t.Errorf("plan = %q, want Go (bare \"go\" planId must resolve)", res.plan)
	}
	credits, ok := res.quotas["Credits"].(map[string]any)
	if !ok {
		t.Fatalf("missing Credits row: %v", res.quotas)
	}
	if credits["total"].(float64) != 10 {
		t.Errorf("Credits total = %v, want the 10-credit Go cap", credits["total"])
	}
	if credits["name"] != "Monthly" {
		t.Errorf("Credits name = %v", credits["name"])
	}
	if credits["percentage"].(float64) != 30 || credits["colorPercentage"].(float64) != 70 {
		t.Errorf("Credits percentages = %v", credits)
	}
	session, ok := res.quotas["Session (5h)"].(map[string]any)
	if !ok {
		t.Fatalf("missing Session (5h) row: %v", res.quotas)
	}
	if session["name"] != "5-hour limit" {
		t.Errorf("session name = %v", session["name"])
	}
	if session["percentage"].(float64) != 20 || session["colorPercentage"].(float64) != 80 {
		t.Errorf("session percentages = %v", session)
	}
	if session["emptyMessage"] != nil {
		t.Errorf("used window must not carry emptyMessage: %v", session)
	}
}

func TestFetchCommandCodeUsage_V1PlanIDAndIdleWindow(t *testing.T) {
	stubCommandCodeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/alpha/whoami":
			w.Write([]byte(`{}`))
		case "/alpha/billing/credits":
			w.Write([]byte(`{"credits":{"monthlyCredits":10,"purchasedCredits":0,"freeCredits":0},
				"windowLimits":{"weekly":{"used":0,"cap":10}}}`))
		case "/alpha/billing/subscriptions":
			w.Write([]byte(`{"data":{"planId":"individual-go-v1"}}`))
		default:
			http.NotFound(w, r)
		}
	})

	res := fetchCommandCodeUsage(context.Background(), "k")
	if res.plan != "Go" {
		t.Errorf("plan = %q, want Go", res.plan)
	}
	weekly, ok := res.quotas["Weekly"].(map[string]any)
	if !ok {
		t.Fatalf("missing Weekly row: %v", res.quotas)
	}
	if weekly["name"] != "Weekly limit" {
		t.Errorf("weekly name = %v", weekly["name"])
	}
	// No usage and no reset reads as idle, not as an unknown reading.
	if weekly["emptyMessage"] != "No usage in this window" {
		t.Errorf("weekly emptyMessage = %v", weekly["emptyMessage"])
	}
}

func TestValidateCommandCodeKey_WhoamiProbe(t *testing.T) {
	srv := stubCommandCodeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/alpha/whoami" {
			// The old generic probe dialed /alpha/generate; the dedicated
			// probe must not.
			t.Errorf("unexpected probe path %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer good-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"org":{}}`))
	})
	defer srv.Close()

	cfg := providers.KnownProviders["commandcode"]
	out := validateProviderKey(context.Background(), "commandcode", cfg, "good-key", nil)
	if !out.valid || !out.supported {
		t.Errorf("valid key rejected: %+v", out)
	}
	bad := validateProviderKey(context.Background(), "commandcode", cfg, "bad-key", nil)
	if bad.valid || bad.message != "Invalid API key" {
		t.Errorf("bad key outcome = %+v", bad)
	}
}
