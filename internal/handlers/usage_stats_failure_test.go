package handlers

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/db"
)

// The usage handler read its three sources behind `if err == nil { ... }` with no
// else branch, so any failure — a dropped table, an unreadable file, a scan error
// — produced a 200 carrying all-zero aggregates. The dashboard rendered that as
// "no traffic in this period" and nothing anywhere logged the fault. A reader
// failure must be a 500 instead.

// statsRequest runs the stats handler and returns the recorder.
func statsRequest(t *testing.T, repo *db.Repo, period string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/usage/stats?period="+period, nil)
	HandleUsageStats(repo)(rec, req)
	return rec
}

// assertStatsRejected fails when the handler reports success for a broken read.
func assertStatsRejected(t *testing.T, period string, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("period=%s: status = %d, want 500. body: %s", period, rec.Code, rec.Body)
	}
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("period=%s: decode error body: %v (raw %s)", period, err, rec.Body)
	}
	if payload.Error.Message == "" {
		t.Errorf("period=%s: 500 body carries no error message: %s", period, rec.Body)
	}
}

func TestUsageStats_BrokenDailyReadIs500NotZeroedTotals(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	// Seed a real row first, so the zeroed response cannot be explained by an
	// empty database.
	if err := repo.UpsertUsageDaily("2026-03-03", `{"byProvider":{"openai":{"requests":7}}}`); err != nil {
		t.Fatalf("seed usageDaily: %v", err)
	}
	if _, err := database.Exec(`DROP TABLE usageDaily;`); err != nil {
		t.Fatalf("drop usageDaily: %v", err)
	}

	// "7d" is a daily window, so it reads usageDaily.
	rec := statsRequest(t, repo, "7d")
	assertStatsRejected(t, "7d", rec)

}

// TestUsageStats_RawHistoryWindowFailureIs500 covers the sub-day branch, which
// reads usageHistory instead of the daily rollup.
func TestUsageStats_RawHistoryWindowFailureIs500(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	if err := repo.InsertUsageHistory("openai", "gpt-5.5", "conn-1", "sk-1", "/v1/chat/completions", 10, 5, 0.5, "success", 15, "{}", "{}"); err != nil {
		t.Fatalf("seed usageHistory: %v", err)
	}
	if _, err := database.Exec(`DROP TABLE usageHistory;`); err != nil {
		t.Fatalf("drop usageHistory: %v", err)
	}

	rec := statsRequest(t, repo, "24h")
	assertStatsRejected(t, "24h", rec)
}

// TestUsageStats_RecentHistoryFailureIs500 covers the recent-requests read,
// which ran independently of the window branch.
func TestUsageStats_RecentHistoryFailureIs500(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	if err := repo.InsertUsageHistory("openai", "gpt-5.5", "conn-1", "sk-1", "/v1/chat/completions", 10, 5, 0.5, "success", 15, "{}", "{}"); err != nil {
		t.Fatalf("seed usageHistory: %v", err)
	}
	if _, err := database.Exec(`DROP TABLE usageHistory;`); err != nil {
		t.Fatalf("drop usageHistory: %v", err)
	}

	// "all" is a daily window, so it reaches the recent-requests read while the
	// daily rollup still resolves.
	rec := statsRequest(t, repo, "all")
	assertStatsRejected(t, "all", rec)
}

// TestUsageStats_HealthyReadStillAggregates guards the other direction: the new
// error branches must not turn a normal dashboard load into a failure.
func TestUsageStats_HealthyReadStillAggregates(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	if err := repo.UpsertUsageDaily("2026-03-03", `{"byProvider":{"openai":{"requests":4,"promptTokens":100,"cost":1.5}}}`); err != nil {
		t.Fatalf("seed usageDaily: %v", err)
	}
	if err := repo.InsertUsageHistory("openai", "gpt-5.5", "conn-1", "sk-1", "/v1/chat/completions", 10, 5, 0.5, "success", 15, "{}", "{}"); err != nil {
		t.Fatalf("seed usageHistory: %v", err)
	}

	rec := statsRequest(t, repo, "24h")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var resp UsageStatsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.TotalRequests == 0 {
		t.Errorf("TotalRequests = 0 on a healthy read; the seeded row was not aggregated: %+v", resp)
	}
	if len(resp.RecentRequests) == 0 {
		t.Errorf("RecentRequests is empty on a healthy read; the seeded row was dropped")
	}
}