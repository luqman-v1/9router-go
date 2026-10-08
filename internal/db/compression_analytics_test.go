package db

import (
	"context"
	"testing"
	"time"
)

func TestCompressionAnalytics_InsertAndSummary(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	if err := EnsureCoreSchema(database); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	repo := NewRepo(database)
	ctx := context.Background()

	// Seed 2 records
	rec1 := CompressionAnalyticsRecord{
		Timestamp:              time.Now().UTC().Format(time.RFC3339),
		Provider:               "anthropic",
		Mode:                   "rtk",
		OriginalTokens:         1000,
		CompressedTokens:       600,
		TokensSaved:            400,
		DurationMs:             30,
		RequestID:              "req-1",
		ActualPromptTokens:     600,
		ActualCompletionTokens: 200,
		ActualTotalTokens:      800,
	}
	if err := repo.InsertCompressionAnalytics(ctx, rec1); err != nil {
		t.Fatalf("insert rec1: %v", err)
	}

	rec2 := CompressionAnalyticsRecord{
		Timestamp:              time.Now().UTC().Format(time.RFC3339),
		Provider:               "openai",
		Mode:                   "caveman",
		OriginalTokens:         500,
		CompressedTokens:       350,
		TokensSaved:            150,
		DurationMs:             20,
		RequestID:              "req-2",
		ActualPromptTokens:     350,
		ActualCompletionTokens: 100,
		ActualTotalTokens:      450,
	}
	if err := repo.InsertCompressionAnalytics(ctx, rec2); err != nil {
		t.Fatalf("insert rec2: %v", err)
	}

	summary, err := repo.GetCompressionAnalyticsSummary(ctx, "24h")
	if err != nil {
		t.Fatalf("get summary: %v", err)
	}

	if summary.TotalRequests != 2 {
		t.Errorf("totalRequests = %d, want 2", summary.TotalRequests)
	}
	if summary.TotalTokensSaved != 550 {
		t.Errorf("totalTokensSaved = %d, want 550", summary.TotalTokensSaved)
	}
	if summary.AvgSavingsPct < 30 || summary.AvgSavingsPct > 40 {
		t.Errorf("avgSavingsPct = %f, expected between 30 and 40", summary.AvgSavingsPct)
	}

	// Verify Mode breakdown
	rtkStats, ok := summary.ByMode["rtk"]
	if !ok || rtkStats.TokensSaved != 400 {
		t.Errorf("byMode[rtk] = %+v, want 400 saved", rtkStats)
	}

	cavemanStats, ok := summary.ByMode["caveman"]
	if !ok || cavemanStats.TokensSaved != 150 {
		t.Errorf("byMode[caveman] = %+v, want 150 saved", cavemanStats)
	}

	// Verify Provider breakdown
	if summary.ByProvider["anthropic"].TokensSaved != 400 {
		t.Errorf("byProvider[anthropic] = %d, want 400", summary.ByProvider["anthropic"].TokensSaved)
	}
	if summary.ByProvider["openai"].TokensSaved != 150 {
		t.Errorf("byProvider[openai] = %d, want 150", summary.ByProvider["openai"].TokensSaved)
	}

	// Verify Real Usage
	if summary.RealUsage.RequestsWithReceipts != 2 {
		t.Errorf("requestsWithReceipts = %d, want 2", summary.RealUsage.RequestsWithReceipts)
	}
	if summary.RealUsage.PromptTokens != 950 {
		t.Errorf("promptTokens = %d, want 950", summary.RealUsage.PromptTokens)
	}

	// Verify ROI and TopSavers
	if summary.RoiTokensPerMs <= 0 {
		t.Errorf("expected roiTokensPerMs > 0, got %f", summary.RoiTokensPerMs)
	}
	if len(summary.TopSavers) != 2 {
		t.Fatalf("expected 2 topSavers, got %d", len(summary.TopSavers))
	}
	if summary.TopSavers[0].TokensSaved != 400 {
		t.Errorf("topSavers[0].TokensSaved = %d, want 400", summary.TopSavers[0].TokensSaved)
	}
}

func TestCompressionAnalytics_UsageHistoryBackfill(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	if err := EnsureCoreSchema(database); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	repo := NewRepo(database)
	ctx := context.Background()

	// Seed usageHistory with saved_tokens using Go 1.22 range loop
	for i := range 3 {
		err := repo.InsertUsageHistory(
			"anthropic", "claude-3-5", "conn-1", "key-1", "/v1/messages",
			600, 200, 0.05, "success", 800, "{}",
			`{"original_input_tokens":1000,"compressed_input_tokens":600,"saved_tokens":400,"saved_percent":40}`,
		)
		if err != nil {
			t.Fatalf("seed usageHistory %d: %v", i, err)
		}
	}

	summary, err := repo.GetCompressionAnalyticsSummary(ctx, "24h")
	if err != nil {
		t.Fatalf("get summary backfill: %v", err)
	}

	if summary.TotalRequests != 3 {
		t.Errorf("totalRequests = %d, want 3", summary.TotalRequests)
	}
	if summary.TotalTokensSaved != 1200 {
		t.Errorf("totalTokensSaved = %d, want 1200", summary.TotalTokensSaved)
	}
	if summary.AvgSavingsPct != 40 {
		t.Errorf("avgSavingsPct = %f, want 40", summary.AvgSavingsPct)
	}
}

func TestBackfillCompressionAnalytics(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	// Ensure core schema (initializes tables)
	if err := EnsureCoreSchema(database); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	// Reset migration flag so we can test explicit backfill execution
	if _, err := database.Exec(`DELETE FROM _meta WHERE key = 'migration_compression_backfill_done'`); err != nil {
		t.Fatalf("reset _meta: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM compressionAnalytics`); err != nil {
		t.Fatalf("clean compressionAnalytics: %v", err)
	}

	// Seed requestDetails
	// 1. RTK + Caveman -> stacked
	d1 := `{"id":"req-c1","latency":{"total":40},"tokens":{"original_input_tokens":1000,"compressed_input_tokens":600,"saved_tokens":400,"prompt_tokens":600,"completion_tokens":100},"request":{"messages":[{"role":"system","content":"terse caveman style active"}]}}`
	if _, err := database.Exec(`INSERT INTO requestDetails (id, timestamp, provider, model, connectionId, status, data) VALUES ('req-c1', '2026-10-06T01:00:00Z', 'anthropic', 'claude-3-5', 'conn-1', 'success', ?)`, d1); err != nil {
		t.Fatalf("seed req-c1: %v", err)
	}

	// 2. RTK + ADHD -> stacked
	d2 := `{"id":"req-a2","latency":{"total":35},"tokens":{"original_input_tokens":800,"compressed_input_tokens":500,"saved_tokens":300,"prompt_tokens":500,"completion_tokens":150},"request":{"messages":[{"role":"system","content":"I have ADHD — action-first output"}]}}`
	if _, err := database.Exec(`INSERT INTO requestDetails (id, timestamp, provider, model, connectionId, status, data) VALUES ('req-a2', '2026-10-06T02:00:00Z', 'openai', 'gpt-4o', 'conn-2', 'success', ?)`, d2); err != nil {
		t.Fatalf("seed req-a2: %v", err)
	}

	// 3. RTK pure -> rtk
	d3 := `{"id":"req-r3","latency":{"total":20},"tokens":{"original_input_tokens":500,"compressed_input_tokens":350,"saved_tokens":150,"prompt_tokens":350,"completion_tokens":50},"request":{"messages":[{"role":"user","content":"git diff"}]}}`
	if _, err := database.Exec(`INSERT INTO requestDetails (id, timestamp, provider, model, connectionId, status, data) VALUES ('req-r3', '2026-10-06T03:00:00Z', 'anthropic', 'claude-3-5', 'conn-1', 'success', ?)`, d3); err != nil {
		t.Fatalf("seed req-r3: %v", err)
	}

	// 4. No savings -> should not be backfilled
	d4 := `{"id":"req-n4","latency":{"total":15},"tokens":{"prompt_tokens":200,"completion_tokens":50,"saved_tokens":0},"request":{"messages":[{"role":"user","content":"hello"}]}}`
	if _, err := database.Exec(`INSERT INTO requestDetails (id, timestamp, provider, model, connectionId, status, data) VALUES ('req-n4', '2026-10-06T04:00:00Z', 'openai', 'gpt-4o', 'conn-2', 'success', ?)`, d4); err != nil {
		t.Fatalf("seed req-n4: %v", err)
	}

	// Run backfill migration
	if err := BackfillCompressionAnalytics(database); err != nil {
		t.Fatalf("BackfillCompressionAnalytics error = %v", err)
	}

	// Check rows in compressionAnalytics
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM compressionAnalytics`).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 3 {
		t.Fatalf("compressionAnalytics count = %d, want 3", count)
	}

	// Check mode classification
	var modeC1, modeA2, modeR3 string
	_ = database.QueryRow(`SELECT mode FROM compressionAnalytics WHERE requestId = 'req-c1'`).Scan(&modeC1)
	_ = database.QueryRow(`SELECT mode FROM compressionAnalytics WHERE requestId = 'req-a2'`).Scan(&modeA2)
	_ = database.QueryRow(`SELECT mode FROM compressionAnalytics WHERE requestId = 'req-r3'`).Scan(&modeR3)

	if modeC1 != "stacked" {
		t.Errorf("req-c1 mode = %q, want 'stacked'", modeC1)
	}
	if modeA2 != "stacked" {
		t.Errorf("req-a2 mode = %q, want 'stacked'", modeA2)
	}
	if modeR3 != "rtk" {
		t.Errorf("req-r3 mode = %q, want 'rtk'", modeR3)
	}

	// Test Idempotency: run backfill again, count should still be 3
	if err := BackfillCompressionAnalytics(database); err != nil {
		t.Fatalf("second backfill error = %v", err)
	}
	var countAfter int
	_ = database.QueryRow(`SELECT COUNT(*) FROM compressionAnalytics`).Scan(&countAfter)
	if countAfter != 3 {
		t.Errorf("count after second run = %d, want 3", countAfter)
	}
}
