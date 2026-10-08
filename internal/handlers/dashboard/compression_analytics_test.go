package dashboard

import (
	"context"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"9router/proxy/internal/db"
	"github.com/go-chi/chi/v5"
)

func TestHandleGetCompressionAnalytics(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	repo := database
	if err := db.EnsureCoreSchema(repo.RawDB()); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	// Seed record
	rec := db.CompressionAnalyticsRecord{
		Timestamp:              time.Now().UTC().Format(time.RFC3339),
		Provider:               "anthropic",
		Mode:                   "rtk",
		OriginalTokens:         1200,
		CompressedTokens:       800,
		TokensSaved:            400,
		DurationMs:             45,
		RequestID:              "req-1",
		ActualPromptTokens:     800,
		ActualCompletionTokens: 200,
		ActualTotalTokens:      1000,
	}
	if err := repo.InsertCompressionAnalytics(context.Background(), rec); err != nil {
		t.Fatalf("insert compression record: %v", err)
	}

	r := chi.NewRouter()
	dashH := NewDashboardHandler(repo)
	RegisterRoutes(r, dashH)

	req := httptest.NewRequest(http.MethodGet, "/api/analytics/compression?since=24h", nil)
	recHTTP := httptest.NewRecorder()
	r.ServeHTTP(recHTTP, req)

	if recHTTP.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", recHTTP.Code, recHTTP.Body.String())
	}

	var summary db.CompressionAnalyticsSummary
	if err := json.Unmarshal(recHTTP.Body.Bytes(), &summary); err != nil {
		t.Fatalf("unmarshal summary: %v", err)
	}

	if summary.TotalRequests != 1 {
		t.Errorf("totalRequests = %d, want 1", summary.TotalRequests)
	}
	if summary.TotalTokensSaved != 400 {
		t.Errorf("totalTokensSaved = %d, want 400", summary.TotalTokensSaved)
	}
	if summary.ByMode["rtk"].TokensSaved != 400 {
		t.Errorf("byMode[rtk].TokensSaved = %d, want 400", summary.ByMode["rtk"].TokensSaved)
	}
}
