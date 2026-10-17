package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchAntigravityDashboardWeekly_HundredScaleAndWindow(t *testing.T) {
	// Mock daily cloudcode server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1internal:retrieveUserQuotaSummary" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"groups": [
					{
						"displayName": "Gemini 4 Argon",
						"buckets": [
							{
								"bucketId": "gemini-argon-wtu-weekly",
								"displayName": "Gemini 4 Argon Weekly Limit",
								"window": "weekly",
								"remainingFraction": 0.98,
								"resetTime": "2026-10-23T09:42:31Z"
							}
						]
					},
					{
						"displayName": "Gemini Models",
						"buckets": [
							{
								"bucketId": "gemini-5h",
								"displayName": "Five Hour Window",
								"window": "5h",
								"remainingFraction": 0.85,
								"resetTime": "2026-10-10T15:00:00Z"
							},
							{
								"bucketId": "gemini-weekly",
								"displayName": "Weekly Window",
								"window": "weekly",
								"remainingFraction": 0.92,
								"resetTime": "2026-10-15T00:00:00Z"
							}
						]
					},
					{
						"displayName": "Claude and GPT Models",
						"buckets": [
							{
								"bucketId": "claude-5h",
								"displayName": "Five Hour Window",
								"window": "5h",
								"remainingFraction": 0.40,
								"resetTime": "2026-10-10T14:30:00Z"
							},
							{
								"bucketId": "claude-weekly",
								"displayName": "Weekly Window",
								"window": "weekly",
								"remainingFraction": 0.65,
								"resetTime": "2026-10-13T08:00:00Z"
							}
						]
					}
				]
			}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	origDaily := antigravityDailyBaseURL
	antigravityDailyBaseURL = srv.URL
	defer func() { antigravityDailyBaseURL = origDaily }()

	weekly := fetchAntigravityDashboardWeekly(context.Background(), "mock-token", "mock-project")
	if len(weekly) != 5 {
		t.Fatalf("expected 5 weekly quota buckets, got %d: %v", len(weekly), weekly)
	}

	tests := []struct {
		key         string
		wantTotal   float64
		wantUsed    float64
		wantRemPct  float64
		wantWindow  string
		wantDisplay string
	}{
		{
			key:         "gemini_session",
			wantTotal:   100.0,
			wantUsed:    15.0,
			wantRemPct:  85.0,
			wantWindow:  "5h",
			wantDisplay: "Gemini (5h)",
		},
		{
			key:         "gemini_weekly",
			wantTotal:   100.0,
			wantUsed:    8.0,
			wantRemPct:  92.0,
			wantWindow:  "weekly",
			wantDisplay: "Gemini (Weekly)",
		},
		{
			key:         "claude_gpt_session",
			wantTotal:   100.0,
			wantUsed:    60.0,
			wantRemPct:  40.0,
			wantWindow:  "5h",
			wantDisplay: "Claude & GPT (5h)",
		},
		{
			key:         "claude_gpt_weekly",
			wantTotal:   100.0,
			wantUsed:    35.0,
			wantRemPct:  65.0,
			wantWindow:  "weekly",
			wantDisplay: "Claude & GPT (Weekly)",
		},
		{
			key:         "argon_weekly",
			wantTotal:   100.0,
			wantUsed:    2.0,
			wantRemPct:  98.0,
			wantWindow:  "weekly",
			wantDisplay: "Gemini 4 Argon (Weekly)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			raw, ok := weekly[tt.key]
			if !ok {
				t.Fatalf("missing key %s in result", tt.key)
			}
			m, ok := raw.(map[string]any)
			if !ok {
				t.Fatalf("key %s is not map[string]any: %v", tt.key, raw)
			}
			if total := m["total"]; total != tt.wantTotal {
				t.Errorf("total = %v, want %v", total, tt.wantTotal)
			}
			if used := m["used"]; used != tt.wantUsed {
				t.Errorf("used = %v, want %v", used, tt.wantUsed)
			}
			if rem := m["remainingPercentage"]; rem != tt.wantRemPct {
				t.Errorf("remainingPercentage = %v, want %v", rem, tt.wantRemPct)
			}
			if win := m["window"]; win != tt.wantWindow {
				t.Errorf("window = %v, want %v", win, tt.wantWindow)
			}
			if disp := m["displayName"]; disp != tt.wantDisplay {
				t.Errorf("displayName = %v, want %v", disp, tt.wantDisplay)
			}
		})
	}
}
