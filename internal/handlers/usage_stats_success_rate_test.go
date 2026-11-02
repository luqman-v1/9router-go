package handlers

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/db"
)

func TestUsageStats_CalculatesSuccessRate(t *testing.T) {
	resetUsageWindowCache()
	t.Cleanup(resetUsageWindowCache)

	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	// Seed 3 successful requests in usageHistory and 1 error request in requestDetails
	for i := 0; i < 3; i++ {
		if err := repo.InsertUsageHistory("openai", "gpt-4o", "c1", "sk-1", "/v1/chat/completions", 10, 10, 0.01, "success", 20, "{}", "{}"); err != nil {
			t.Fatalf("insert success: %v", err)
		}
	}
	if err := repo.InsertRequestDetail("req-err-1", "openai", "gpt-4o", "c1", "error", "{}"); err != nil {
		t.Fatalf("insert error: %v", err)
	}

	handler := HandleUsageStats(repo)
	req := httptest.NewRequest(http.MethodGet, "/api/usage/stats?period=24h", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rec.Code, rec.Body.String())
	}

	var resp UsageStatsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp.TotalRequests != 4 {
		t.Errorf("expected 4 total requests, got %d", resp.TotalRequests)
	}
	if resp.TotalErrors != 1 {
		t.Errorf("expected 1 total error, got %d", resp.TotalErrors)
	}
	if resp.SuccessRate == nil || *resp.SuccessRate != 75.0 {
		t.Errorf("expected 75%% success rate, got %v", resp.SuccessRate)
	}
}
