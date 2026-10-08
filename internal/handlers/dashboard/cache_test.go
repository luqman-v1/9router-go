package dashboard

import (
	"context"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/semanticcache"
	"9router/proxy/internal/translator"
	"github.com/go-chi/chi/v5"
)

func TestHandleGetCache(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	repo := database
	if err := db.EnsureCoreSchema(repo.RawDB()); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	sc := semanticcache.New(semanticcache.Config{
		Enabled:    true,
		TTL:        1 * time.Hour,
		MaxEntries: 100,
	}, nil)

	// Seed semantic cache entry
	req := &translator.OpenAIRequest{
		Model: "gpt-4o",
		Messages: []translator.OpenAIMessage{
			{Role: "user", Content: "test query"},
		},
	}
	respBody := []byte(`{"id":"chatcmpl-test","usage":{"completion_tokens":20,"total_tokens":50}}`)
	if err := sc.Store(context.Background(), req, respBody, "application/json"); err != nil {
		t.Fatalf("store semantic cache: %v", err)
	}

	// Trigger 1 hit and 1 miss
	_, _, _ = sc.Lookup(context.Background(), req)
	missReq := &translator.OpenAIRequest{Model: "gpt-4o", Messages: []translator.OpenAIMessage{{Role: "user", Content: "not found"}}}
	_, _, _ = sc.Lookup(context.Background(), missReq)

	// Seed usage history with prompt cache tokens
	err := repo.InsertUsageHistory(
		"anthropic", "claude-3-5", "conn_1", "key_1", "/v1/messages",
		1200, 300, 0.05, "success", 1500, "{}",
		`{"prompt_tokens":1200,"completion_tokens":300,"cached_tokens":1000,"cache_creation_input_tokens":100}`,
	)
	if err != nil {
		t.Fatalf("seed usage history: %v", err)
	}

	r := chi.NewRouter()
	dashH := NewDashboardHandler(repo)
	dashH.SemanticCache = sc
	RegisterRoutes(r, dashH)

	reqHTTP := httptest.NewRequest(http.MethodGet, "/api/cache?trendHours=24", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, reqHTTP)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}

	var resp CacheStatsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	// Verify SemanticCache section
	if resp.SemanticCache.Hits != 1 {
		t.Errorf("semanticCache.Hits = %d, want 1", resp.SemanticCache.Hits)
	}
	if resp.SemanticCache.Misses != 1 {
		t.Errorf("semanticCache.Misses = %d, want 1", resp.SemanticCache.Misses)
	}
	if resp.SemanticCache.TokensSaved != 50 {
		t.Errorf("semanticCache.TokensSaved = %d, want 50", resp.SemanticCache.TokensSaved)
	}

	// Verify PromptCache section
	if resp.PromptCache == nil {
		t.Fatal("expected non-nil PromptCache")
	}
	if resp.PromptCache.TotalRequests != 1 {
		t.Errorf("promptCache.TotalRequests = %d, want 1", resp.PromptCache.TotalRequests)
	}
	if resp.PromptCache.TotalCachedTokens != 1000 {
		t.Errorf("promptCache.TotalCachedTokens = %d, want 1000", resp.PromptCache.TotalCachedTokens)
	}
	if resp.PromptCache.TokensSaved != 1000 {
		t.Errorf("promptCache.TokensSaved = %d, want 1000", resp.PromptCache.TokensSaved)
	}
	if resp.PromptCache.ByProvider["anthropic"].CachedTokens != 1000 {
		t.Errorf("anthropic CachedTokens = %d, want 1000", resp.PromptCache.ByProvider["anthropic"].CachedTokens)
	}

	// Verify Trend
	if len(resp.Trend) == 0 {
		t.Fatal("expected non-empty trend")
	}
}

func TestHandleGetCacheEntries_And_Delete(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	sc := semanticcache.New(semanticcache.Config{
		Enabled:    true,
		TTL:        1 * time.Hour,
		MaxEntries: 100,
	}, nil)

	// Seed 3 entries using Go 1.22 range loop
	for i := range 3 {
		req := &translator.OpenAIRequest{
			Model: "gpt-4o",
			Messages: []translator.OpenAIMessage{
				{Role: "user", Content: string(rune('a' + i))},
			},
		}
		body := []byte(`{"id":"chatcmpl","usage":{"completion_tokens":10,"total_tokens":30}}`)
		if err := sc.Store(context.Background(), req, body, "application/json"); err != nil {
			t.Fatalf("seed entry %d: %v", i, err)
		}
	}

	r := chi.NewRouter()
	dashH := NewDashboardHandler(database)
	dashH.SemanticCache = sc
	RegisterRoutes(r, dashH)

	// 1. List entries
	reqHTTP := httptest.NewRequest(http.MethodGet, "/api/cache/entries?page=1&limit=10", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, reqHTTP)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}

	var listResp CacheEntriesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("unmarshal entries: %v", err)
	}
	if listResp.Pagination.Total != 3 {
		t.Errorf("total = %d, want 3", listResp.Pagination.Total)
	}
	if len(listResp.Entries) != 3 {
		t.Fatalf("entries count = %d, want 3", len(listResp.Entries))
	}

	// 2. Delete entry by ID
	targetID := listResp.Entries[0].ID
	delReq := httptest.NewRequest(http.MethodDelete, "/api/cache/entries?id="+targetID, nil)
	delRec := httptest.NewRecorder()
	r.ServeHTTP(delRec, delReq)

	if delRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", delRec.Code, delRec.Body.String())
	}
	if sc.Len() != 2 {
		t.Errorf("sc.Len = %d after delete, want 2", sc.Len())
	}

	// 3. Clear all cache entries
	clearReq := httptest.NewRequest(http.MethodDelete, "/api/cache", nil)
	clearRec := httptest.NewRecorder()
	r.ServeHTTP(clearRec, clearReq)

	if clearRec.Code != http.StatusOK {
		t.Fatalf("clear status = %d, body = %s", clearRec.Code, clearRec.Body.String())
	}
	if sc.Len() != 0 {
		t.Errorf("sc.Len = %d after clear, want 0", sc.Len())
	}
}
