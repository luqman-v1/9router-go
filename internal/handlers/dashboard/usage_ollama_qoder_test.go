package dashboard

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A free Ollama account reports `limits.monthly` and nothing else. The port
// originally read only session/weekly, so those accounts were told "no usage
// limits reported" while a real quota sat in the response.
func TestNextMonthlyResetFromSignup(t *testing.T) {
	tests := []struct {
		name     string
		created  string
		now      string
		want     string
		wantZero bool
	}{
		{
			name:    "next month when the signup anniversary is ahead",
			created: "2025-08-14T15:08:01.464687Z",
			now:     "2026-09-27T13:52:00Z",
			want:    "2026-10-14T15:08:01Z",
		},
		{
			name:    "same month rolls forward",
			created: "2025-08-14T15:08:01Z",
			now:     "2025-08-20T00:00:00Z",
			want:    "2025-09-14T15:08:01Z",
		},
		{
			name:    "day clamps to a shorter month",
			created: "2025-01-31T10:00:00Z",
			now:     "2025-01-31T11:00:00Z",
			want:    "2025-02-28T10:00:00Z",
		},
		{
			name:    "day clamps into a leap February",
			created: "2024-01-31T10:00:00Z",
			now:     "2024-01-31T11:00:00Z",
			want:    "2024-02-29T10:00:00Z",
		},
		{
			name:    "year rollover",
			created: "2025-12-10T00:00:00Z",
			now:     "2025-12-20T00:00:00Z",
			want:    "2026-01-10T00:00:00Z",
		},
		{
			name:     "unparseable signup date yields no reset",
			created:  "not-a-date",
			now:      "2026-09-27T00:00:00Z",
			wantZero: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now, err := time.Parse(time.RFC3339, tt.now)
			if err != nil {
				t.Fatalf("bad fixture now: %v", err)
			}
			got := nextMonthlyResetFromSignup(tt.created, now)
			if tt.wantZero {
				if got != "" {
					t.Errorf("expected no reset, got %q", got)
				}
				return
			}
			if got != tt.want {
				t.Errorf("nextMonthlyResetFromSignup() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOllamaRatioQuota(t *testing.T) {
	tests := []struct {
		name     string
		ratio    float64
		wantUsed float64
		wantPct  float64
		resetAt  string
	}{
		{name: "zero usage", ratio: 0, wantUsed: 0, wantPct: 100},
		{name: "quarter used", ratio: 0.25, wantUsed: 25, wantPct: 75},
		{name: "half used", ratio: 0.5, wantUsed: 50, wantPct: 50},
		{name: "ratio clamps above one", ratio: 1.4, wantUsed: 100, wantPct: 0},
		{name: "negative clamps to zero", ratio: -0.2, wantUsed: 0, wantPct: 100},
		{name: "reset passes through", ratio: 0.25, wantUsed: 25, wantPct: 75, resetAt: "2026-10-14T15:08:01Z"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := ollamaRatioQuota(tt.ratio, tt.resetAt)
			if q["used"] != tt.wantUsed {
				t.Errorf("used = %v, want %v", q["used"], tt.wantUsed)
			}
			if q["total"] != float64(100) {
				t.Errorf("total = %v, want 100", q["total"])
			}
			if q["remainingPercentage"] != tt.wantPct {
				t.Errorf("remainingPercentage = %v, want %v", q["remainingPercentage"], tt.wantPct)
			}
			if tt.resetAt == "" {
				if q["resetAt"] != nil {
					t.Errorf("resetAt = %v, want nil", q["resetAt"])
				}
			} else if q["resetAt"] != tt.resetAt {
				t.Errorf("resetAt = %v, want %q", q["resetAt"], tt.resetAt)
			}
		})
	}
}

// monthly must stay in this list: it is the only window a free account reports.
func TestOllamaLimitWindows_CoverUpstreamWindows(t *testing.T) {
	want := map[string]bool{"session": false, "weekly": false, "monthly": false}
	for _, w := range ollamaLimitWindows {
		if _, ok := want[w.Key]; !ok {
			t.Errorf("window %q is not in upstream OLLAMA_LIMIT_WINDOWS", w.Key)
			continue
		}
		want[w.Key] = true
	}
	for key, seen := range want {
		if !seen {
			t.Errorf("window %q missing from ollamaLimitWindows", key)
		}
	}
}

func TestFetchOllamaUsage_ReportsMonthlyForFreePlan(t *testing.T) {
	withOllamaEndpoints(t,
		`{"limits":{"monthly":{"usage":0.25,"models":[]}},"activity":{"cost":"0.1"}}`,
		`{"Plan":"free","CreatedAt":"2025-08-14T15:08:01.464687Z"}`)

	res := fetchOllamaUsage(t.Context(), "ollama-key")
	if res.message != "" {
		t.Fatalf("unexpected message: %q", res.message)
	}
	row, ok := res.quotas["Monthly"].(map[string]any)
	if !ok {
		t.Fatalf("expected a Monthly row, got %v", res.quotas)
	}
	if row["used"] != float64(25) {
		t.Errorf("used = %v, want 25", row["used"])
	}
	if _, hasSession := res.quotas["Session (5h)"]; hasSession {
		t.Error("a free plan must not invent a session row")
	}
	// Free plan: the reset is derived from the signup date, since Ollama
	// exposes no reset timestamp for the monthly window.
	resetAt, _ := row["resetAt"].(string)
	if resetAt == "" {
		t.Error("expected a derived monthly reset on the free plan")
	} else if _, err := time.Parse(time.RFC3339, resetAt); err != nil {
		t.Errorf("resetAt %q is not RFC3339: %v", resetAt, err)
	}
}

func TestFetchOllamaUsage_PaidPlanHasSessionAndWeekly(t *testing.T) {
	withOllamaEndpoints(t,
		`{"limits":{"session":{"usage":0.1},"weekly":{"usage":0.2},"monthly":{"usage":0.3}}}`,
		`{"Plan":"pro","CreatedAt":"2025-01-01T00:00:00Z"}`)

	res := fetchOllamaUsage(t.Context(), "ollama-key")
	for _, label := range []string{"Session (5h)", "Weekly (7d)", "Monthly"} {
		if _, ok := res.quotas[label]; !ok {
			t.Errorf("expected a %q row, got %v", label, res.quotas)
		}
	}
	// A paid plan is not billed monthly from signup, so no derived reset.
	monthly, _ := res.quotas["Monthly"].(map[string]any)
	if monthly["resetAt"] != nil {
		t.Errorf("paid plan monthly resetAt = %v, want nil", monthly["resetAt"])
	}
}

func TestFetchOllamaUsage_NoWindowsStillReportsMessage(t *testing.T) {
	withOllamaEndpoints(t, `{"limits":{}}`, `{"Plan":"free"}`)

	res := fetchOllamaUsage(t.Context(), "ollama-key")
	if res.message == "" {
		t.Error("expected the no-limits message when no window is reported")
	}
	if len(res.quotas) != 0 {
		t.Errorf("expected no rows, got %v", res.quotas)
	}
}

// A 401 is a dead token, not a rate limit, and Qoder device tokens cannot be
// refreshed — the card must say so instead of showing a bare status code.
func TestFetchQoderUsage_ExpiredTokenSaysReauthorize(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{}`))
		}))

		res := fetchQoderUsageAt(t.Context(), "dead-token", srv.URL)
		if res.message != "Qoder authentication expired. Please re-authorize this connection." {
			t.Errorf("status %d: message = %q", status, res.message)
		}
		if res.plan != "Qoder" {
			t.Errorf("status %d: plan = %q, want Qoder", status, res.plan)
		}
		if !res.bare {
			t.Errorf("status %d: expected a bare result so no empty table renders", status)
		}
		srv.Close()
	}
}

func TestFetchQoderUsage_NonAuthStatusKeepsStatusMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	res := fetchQoderUsageAt(t.Context(), "tok", srv.URL)
	if res.message != "Qoder connected. Usage fetch returned 429." {
		t.Errorf("message = %q, want the 429 status message", res.message)
	}
}

func withOllamaEndpoints(t *testing.T, usageBody, meBody string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/usage", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(usageBody))
	})
	mux.HandleFunc("/api/me", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(meBody))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	prevUsage, prevMe := ollamaUsageURL, ollamaMeURL
	ollamaUsageURL, ollamaMeURL = srv.URL+"/api/usage", srv.URL+"/api/me"
	t.Cleanup(func() { ollamaUsageURL, ollamaMeURL = prevUsage, prevMe })
}

func TestFetchQoderUsage_ParsesAddOnQuota(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"userQuota": {"total": 1000, "used": 200, "remaining": 800, "unit": "credits"},
			"orgResourcePackage": {"total": 5000, "used": 1000, "remaining": 4000, "unit": "credits"},
			"addOnQuota": {"total": 2200, "used": 150, "remaining": 2050, "unit": "credits", "detailUrl": "https://qoder.sh/user/wallet"},
			"totalUsagePercentage": 25,
			"isQuotaExceeded": false
		}`))
	}))
	defer srv.Close()

	res := fetchQoderUsageAt(t.Context(), "valid-token", srv.URL)
	if res.message != "" {
		t.Fatalf("unexpected message: %s", res.message)
	}

	addOn, ok := res.quotas["addOn"].(map[string]any)
	if !ok {
		t.Fatalf("expected addOn quota in %v", res.quotas)
	}

	if addOn["total"] != float64(2200) {
		t.Errorf("addOn total = %v, want 2200", addOn["total"])
	}
	if addOn["used"] != float64(150) {
		t.Errorf("addOn used = %v, want 150", addOn["used"])
	}
	if addOn["remaining"] != float64(2050) {
		t.Errorf("addOn remaining = %v, want 2050", addOn["remaining"])
	}
	if addOn["unit"] != "credits" {
		t.Errorf("addOn unit = %v, want credits", addOn["unit"])
	}
	if addOn["detailUrl"] != "https://qoder.sh/user/wallet" {
		t.Errorf("addOn detailUrl = %v, want https://qoder.sh/user/wallet", addOn["detailUrl"])
	}
}

func TestFetchQoderUsage_OmittedAddOnQuotaWhenZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"userQuota": {"total": 1000, "used": 200, "remaining": 800, "unit": "credits"},
			"orgResourcePackage": {"total": 5000, "used": 1000, "remaining": 4000, "unit": "credits"},
			"addOnQuota": {"total": 0, "used": 0, "remaining": 0}
		}`))
	}))
	defer srv.Close()

	res := fetchQoderUsageAt(t.Context(), "valid-token", srv.URL)
	if _, ok := res.quotas["addOn"]; ok {
		t.Errorf("addOn quota should be omitted when total is 0: %v", res.quotas)
	}
}
