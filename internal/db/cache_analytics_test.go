package db

import (
	"context"
	"testing"
)

func setupAnalyticsTestDB(t *testing.T) (*Repo, func()) {
	database, cleanup := setupTestDB(t)
	if err := EnsureCoreSchema(database); err != nil {
		cleanup()
		t.Fatalf("EnsureCoreSchema failed: %v", err)
	}
	return NewRepo(database), cleanup
}

func TestGetPromptCacheMetrics_EmptyDB(t *testing.T) {
	repo, cleanup := setupAnalyticsTestDB(t)
	defer cleanup()

	metrics, err := repo.GetPromptCacheMetrics(context.Background())
	if err != nil {
		t.Fatalf("GetPromptCacheMetrics error = %v, want nil", err)
	}
	if metrics == nil {
		t.Fatal("expected non-nil metrics")
	}
	if metrics.TotalRequests != 0 {
		t.Errorf("TotalRequests = %d, want 0", metrics.TotalRequests)
	}
	if metrics.TotalCachedTokens != 0 {
		t.Errorf("TotalCachedTokens = %d, want 0", metrics.TotalCachedTokens)
	}
	if metrics.TokensSaved != 0 {
		t.Errorf("TokensSaved = %d, want 0", metrics.TokensSaved)
	}
	if metrics.EstimatedCostSaved != 0 {
		t.Errorf("EstimatedCostSaved = %f, want 0", metrics.EstimatedCostSaved)
	}
	if len(metrics.ByProvider) != 0 {
		t.Errorf("ByProvider len = %d, want 0", len(metrics.ByProvider))
	}
}

func TestGetPromptCacheMetrics_WithData(t *testing.T) {
	repo, cleanup := setupAnalyticsTestDB(t)
	defer cleanup()

	// Seed rows
	// Row 1: Anthropic with cached tokens
	err := repo.InsertUsageHistory(
		"anthropic", "claude-3-5-sonnet", "conn_1", "key_1", "/v1/messages",
		1000, 200, 0.05, "success", 1200, "{}",
		`{"prompt_tokens":1000,"completion_tokens":200,"cached_tokens":800,"cache_creation_input_tokens":100}`,
	)
	if err != nil {
		t.Fatalf("seed row 1: %v", err)
	}

	// Row 2: OpenAI with cached tokens
	err = repo.InsertUsageHistory(
		"openai", "gpt-4o", "conn_2", "key_2", "/v1/chat/completions",
		500, 100, 0.01, "success", 600, "{}",
		`{"prompt_tokens":500,"completion_tokens":100,"cached_tokens":200,"cache_creation_input_tokens":0}`,
	)
	if err != nil {
		t.Fatalf("seed row 2: %v", err)
	}

	// Row 3: OpenAI without cache
	err = repo.InsertUsageHistory(
		"openai", "gpt-4o", "conn_2", "key_2", "/v1/chat/completions",
		300, 50, 0.005, "success", 350, "{}",
		`{"prompt_tokens":300,"completion_tokens":50,"cached_tokens":0}`,
	)
	if err != nil {
		t.Fatalf("seed row 3: %v", err)
	}

	// Row 4: Non-JSON / empty tokens string (must not crash json_valid)
	err = repo.InsertUsageHistory(
		"gemini", "gemini-pro", "conn_3", "key_3", "/v1/chat/completions",
		200, 50, 0.002, "success", 250, "{}",
		`raw_non_json_string`,
	)
	if err != nil {
		t.Fatalf("seed row 4: %v", err)
	}

	metrics, err := repo.GetPromptCacheMetrics(context.Background())
	if err != nil {
		t.Fatalf("GetPromptCacheMetrics error = %v", err)
	}

	if metrics.TotalRequests != 4 {
		t.Errorf("TotalRequests = %d, want 4", metrics.TotalRequests)
	}
	if metrics.RequestsWithCacheControl != 2 {
		t.Errorf("RequestsWithCacheControl = %d, want 2", metrics.RequestsWithCacheControl)
	}
	if metrics.TotalInputTokens != 2000 {
		t.Errorf("TotalInputTokens = %d, want 2000", metrics.TotalInputTokens)
	}
	if metrics.TotalCachedTokens != 1000 {
		t.Errorf("TotalCachedTokens = %d, want 1000", metrics.TotalCachedTokens)
	}
	if metrics.TotalCacheCreationTokens != 400 {
		t.Errorf("TotalCacheCreationTokens = %d, want 400 (100 explicit + 300 fallback)", metrics.TotalCacheCreationTokens)
	}
	if metrics.TokensSaved != 1000 {
		t.Errorf("TokensSaved = %d, want 1000", metrics.TokensSaved)
	}

	// Check provider breakdown
	anthropicStats, ok := metrics.ByProvider["anthropic"]
	if !ok {
		t.Fatal("expected anthropic in ByProvider")
	}
	if anthropicStats.CachedTokens != 800 {
		t.Errorf("anthropic CachedTokens = %d, want 800", anthropicStats.CachedTokens)
	}
	if anthropicStats.CacheCreationTokens != 100 {
		t.Errorf("anthropic CacheCreationTokens = %d, want 100", anthropicStats.CacheCreationTokens)
	}
	if anthropicStats.CachedRequests != 1 {
		t.Errorf("anthropic CachedRequests = %d, want 1", anthropicStats.CachedRequests)
	}

	openaiStats, ok := metrics.ByProvider["openai"]
	if !ok {
		t.Fatal("expected openai in ByProvider")
	}
	if openaiStats.TotalRequests != 2 {
		t.Errorf("openai TotalRequests = %d, want 2", openaiStats.TotalRequests)
	}
	if openaiStats.CachedRequests != 1 {
		t.Errorf("openai CachedRequests = %d, want 1", openaiStats.CachedRequests)
	}
	if openaiStats.CachedTokens != 200 {
		t.Errorf("openai CachedTokens = %d, want 200", openaiStats.CachedTokens)
	}

	// Check model breakdown
	sonnetStats, ok := metrics.ByModel["claude-3-5-sonnet"]
	if !ok {
		t.Fatal("expected claude-3-5-sonnet in ByModel")
	}
	if sonnetStats.CachedTokens != 800 {
		t.Errorf("claude-3-5-sonnet CachedTokens = %d, want 800", sonnetStats.CachedTokens)
	}
}

func TestGetPromptCacheTrend(t *testing.T) {
	repo, cleanup := setupAnalyticsTestDB(t)
	defer cleanup()

	// Seed rows using Go 1.22 integer range loop
	for range 3 {
		err := repo.InsertUsageHistory(
			"anthropic", "claude-3-5", "conn_1", "key_1", "/v1/messages",
			500, 100, 0.01, "success", 600, "{}",
			`{"cached_tokens":300,"cache_creation_input_tokens":50}`,
		)
		if err != nil {
			t.Fatalf("seed row: %v", err)
		}
	}

	points, err := repo.GetPromptCacheTrend(context.Background(), 24)
	if err != nil {
		t.Fatalf("GetPromptCacheTrend error = %v", err)
	}
	if len(points) == 0 {
		t.Fatal("expected trend points, got 0")
	}

	totalCached := int64(0)
	for _, p := range points {
		totalCached += p.CachedTokens
	}
	if totalCached != 900 {
		t.Errorf("totalCached across trend = %d, want 900", totalCached)
	}
}

// The trend buckets a row into "used a cache" from two different columns of the
// tokens JSON — the read-back count and the creation count. A rewrite of that
// CASE expression can silently collapse to a constant, which would report the
// same cachedRequests for an hour that cached everything and an hour that
// cached nothing.
func TestGetPromptCacheTrend_CountsBothCacheTokenSources(t *testing.T) {
	repo, cleanup := setupAnalyticsTestDB(t)
	defer cleanup()

	seed := func(tokens string) {
		t.Helper()
		if err := repo.InsertUsageHistory(
			"anthropic", "claude-3-5", "conn_1", "key_1", "/v1/messages",
			500, 100, 0.01, "success", 600, "{}", tokens,
		); err != nil {
			t.Fatalf("seed row: %v", err)
		}
	}
	seed(`{"cached_tokens":300}`)
	seed(`{"cache_creation_input_tokens":120}`)
	seed(`{}`)
	// A payload that is not JSON must not be counted as a cache hit, and must
	// not fail the aggregate either.
	seed(`raw_non_json_string`)

	points, err := repo.GetPromptCacheTrend(context.Background(), 24)
	if err != nil {
		t.Fatalf("GetPromptCacheTrend error = %v", err)
	}

	var requests, cachedRequests int64
	for _, p := range points {
		requests += p.Requests
		cachedRequests += p.CachedRequests
	}
	if requests != 4 {
		t.Errorf("requests across trend = %d, want 4", requests)
	}
	// A read-back row and a creation-only row both count as cache traffic; the
	// empty payload and the non-JSON one must not.
	if cachedRequests != 2 {
		t.Errorf("cachedRequests across trend = %d, want 2: a creation-only row is a cache hit too", cachedRequests)
	}
}
