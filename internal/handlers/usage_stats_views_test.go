package handlers

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/db"
)

// The ?views= parameter exists so the dashboard's five-second poll can ask for
// the totals alone. The four per-model breakdown maps are ~98% of the payload
// at 411 models, and the poll's consumers — the KPI row and the live pulse —
// read none of them. Measured on a 305k-row ledger: 415 KB per poll before,
// 5 KB after.
//
// The default must not change, because every other caller (and every existing
// client) reads the full shape.
func TestHandleUsageStats_ViewsSelectsBreakdowns(t *testing.T) {
	seed := func(t *testing.T) *db.Repo {
		t.Helper()
		database, cleanup := setupTestDB(t)
		t.Cleanup(cleanup)
		repo := db.NewRepo(database)
		dailyJSON := `{
			"byProvider": {
				"openai": {"requests": 5, "promptTokens": 100, "completionTokens": 50, "cost": 0.002}
			},
			"byModel": {
				"gpt-4o|openai": {"rawModel": "gpt-4o", "provider": "openai", "requests": 5, "promptTokens": 100, "completionTokens": 50, "cost": 0.002}
			},
			"byAccount": {
				"conn-1|gpt-4o|openai": {"rawModel": "gpt-4o", "provider": "openai", "requests": 5, "promptTokens": 100, "completionTokens": 50, "cost": 0.002}
			},
			"byApiKey": {
				"sk-test|gpt-4o|openai": {"rawModel": "gpt-4o", "provider": "openai", "apiKey": "sk-test", "requests": 5, "promptTokens": 100, "completionTokens": 50, "cost": 0.002}
			},
			"byEndpoint": {
				"/v1/chat/completions|gpt-4o|openai": {"endpoint": "/v1/chat/completions", "rawModel": "gpt-4o", "provider": "openai", "requests": 5, "promptTokens": 100, "completionTokens": 50, "cost": 0.002}
			}
		}`
		if _, err := database.Exec(`INSERT INTO usageDaily (dateKey, data) VALUES ('2026-10-14', ?)`, dailyJSON); err != nil {
			t.Fatalf("seed usageDaily: %v", err)
		}
		return repo
	}

	read := func(t *testing.T, repo *db.Repo, query string) UsageStatsResponse {
		t.Helper()
		rec := httptest.NewRecorder()
		HandleUsageStats(repo)(rec, httptest.NewRequest(http.MethodGet, "/api/usage/stats?"+query, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		var resp UsageStatsResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return resp
	}

	t.Run("no views parameter keeps every breakdown", func(t *testing.T) {
		resp := read(t, seed(t), "period=7d")
		if len(resp.ByModel) == 0 || len(resp.ByAccount) == 0 || len(resp.ByApiKey) == 0 || len(resp.ByEndpoint) == 0 {
			t.Fatalf("default shape lost a breakdown: model=%d account=%d apiKey=%d endpoint=%d",
				len(resp.ByModel), len(resp.ByAccount), len(resp.ByApiKey), len(resp.ByEndpoint))
		}
	})

	t.Run("views=none keeps the totals and drops the breakdowns", func(t *testing.T) {
		repo := seed(t)
		full := read(t, repo, "period=7d")
		totalsOnly := read(t, repo, "period=7d&views=none")

		// The totals are what the poll actually draws, so they must be exact —
		// not merely non-zero. byProvider is the source the totals are summed
		// from, so it is built even here.
		if totalsOnly.TotalRequests != full.TotalRequests {
			t.Errorf("totalRequests = %d, want %d", totalsOnly.TotalRequests, full.TotalRequests)
		}
		if totalsOnly.TotalPromptTokens != full.TotalPromptTokens ||
			totalsOnly.TotalCompletionTokens != full.TotalCompletionTokens {
			t.Errorf("token totals drifted: %+v vs %+v", totalsOnly, full)
		}
		if len(totalsOnly.ByProvider) != len(full.ByProvider) {
			t.Errorf("byProvider = %d rows, want %d (the totals are summed from it)",
				len(totalsOnly.ByProvider), len(full.ByProvider))
		}
		if len(totalsOnly.ByModel) != 0 || len(totalsOnly.ByAccount) != 0 ||
			len(totalsOnly.ByApiKey) != 0 || len(totalsOnly.ByEndpoint) != 0 {
			t.Errorf("views=none built a breakdown: model=%d account=%d apiKey=%d endpoint=%d",
				len(totalsOnly.ByModel), len(totalsOnly.ByAccount), len(totalsOnly.ByApiKey), len(totalsOnly.ByEndpoint))
		}
	})

	t.Run("a single named view builds only that breakdown", func(t *testing.T) {
		resp := read(t, seed(t), "period=7d&views=model")
		if len(resp.ByModel) == 0 {
			t.Error("views=model did not build byModel")
		}
		if len(resp.ByAccount) != 0 || len(resp.ByApiKey) != 0 || len(resp.ByEndpoint) != 0 {
			t.Errorf("views=model also built others: account=%d apiKey=%d endpoint=%d",
				len(resp.ByAccount), len(resp.ByApiKey), len(resp.ByEndpoint))
		}
	})

	t.Run("an unknown view name is ignored rather than rejected", func(t *testing.T) {
		// The endpoint is a display feed: a typo should cost a missing table,
		// not a failed read that blanks the whole page.
		resp := read(t, seed(t), "period=7d&views=nonsense")
		if resp.TotalRequests == 0 {
			t.Error("an unknown view name dropped the totals")
		}
	})
}
