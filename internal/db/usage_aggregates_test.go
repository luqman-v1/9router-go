package db

import (
	"testing"
	"time"
)

// The Details section's Top Model card and its cache-hit rate are aggregates
// over the trailing week. They used to be folded in the process, which meant
// every row of the window — and the JSON payload each row carries — was
// shipped into memory to sum two numbers. They run in SQL now, and the tests
// below pin the counting rules the SQL keeps: the cache-inclusive row, the
// cache-exclusive row, the uncached row that stays out of the denominator
// entirely, and the cache-only import that is not a hit at all.

func seedUsageAggregateRow(t *testing.T, repo *Repo, age time.Duration, provider, model string, prompt int, tokens string) {
	t.Helper()
	ts := time.Now().UTC().Add(-age).Format(time.RFC3339)
	if _, err := repo.RawDB().Exec(
		`INSERT INTO usageHistory (timestamp, provider, model, promptTokens, completionTokens, tokens)
		 VALUES (?, ?, ?, ?, 0, ?)`,
		ts, provider, model, prompt, tokens,
	); err != nil {
		t.Fatalf("seed usage row: %v", err)
	}
}

func usageAggregateRepo(t *testing.T) (*Repo, func()) {
	t.Helper()
	database, cleanup := setupTestDB(t)
	if err := EnsureCoreSchema(database); err != nil {
		cleanup()
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	return NewRepo(database), cleanup
}

func TestGetUsageCacheHitTotalsSince_CountsCachedAndUncachedRows(t *testing.T) {
	repo, cleanup := usageAggregateRepo(t)
	defer cleanup()

	// Cache-inclusive canonical row: 1000 input of which 400 cached.
	seedUsageAggregateRow(t, repo, time.Hour, "anthropic", "claude", 1000,
		`{"prompt_tokens":1000,"cached_tokens":400,"cache_creation_input_tokens":100}`)
	// Cache-exclusive raw row: prompt 8 next to a huge cache read.
	seedUsageAggregateRow(t, repo, 2*time.Hour, "anthropic", "claude", 8,
		`{"prompt_tokens":8,"cache_read_input_tokens":263296,"cache_creation_input_tokens":0}`)
	// Uncached row stays out of the denominator entirely.
	seedUsageAggregateRow(t, repo, 3*time.Hour, "anthropic", "claude", 500,
		`{"prompt_tokens":500}`)

	hit, total, err := repo.GetUsageCacheHitTotalsSince(time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339))
	if err != nil {
		t.Fatalf("GetUsageCacheHitTotalsSince: %v", err)
	}
	// hit = 400 + 263296; total = max(1000, 400+100) + max(8, 263296) = 1000 + 263296.
	if want := int64(400 + 263296); hit != want {
		t.Errorf("hit = %d, want %d", hit, want)
	}
	if want := int64(1000 + 263296); total != want {
		t.Errorf("total = %d, want %d", total, want)
	}
}

func TestGetUsageCacheHitTotalsSince_NoCacheMeansNoRate(t *testing.T) {
	repo, cleanup := usageAggregateRepo(t)
	defer cleanup()

	seedUsageAggregateRow(t, repo, time.Hour, "openai", "gpt-5.5", 10, `{"prompt_tokens":10}`)

	hit, total, err := repo.GetUsageCacheHitTotalsSince(time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339))
	if err != nil {
		t.Fatalf("GetUsageCacheHitTotalsSince: %v", err)
	}
	if hit != 0 || total != 0 {
		t.Errorf("hit = %d, total = %d, want 0/0 when no request has a cache", hit, total)
	}
}

func TestGetUsageCacheHitTotalsSince_CacheOnlyInputIsNotAHit(t *testing.T) {
	repo, cleanup := usageAggregateRepo(t)
	defer cleanup()

	// cached == prompt with no writes is a distorted import: cache-only input
	// would report a 100% hit rate the provider never served.
	seedUsageAggregateRow(t, repo, time.Hour, "openai", "gpt-5.5", 500,
		`{"cached_tokens":500}`)

	hit, total, err := repo.GetUsageCacheHitTotalsSince(time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339))
	if err != nil {
		t.Fatalf("GetUsageCacheHitTotalsSince: %v", err)
	}
	if hit != 0 || total != 0 {
		t.Errorf("hit = %d, total = %d, want 0/0 when every row is cache-only input", hit, total)
	}
}

func TestGetUsageCacheHitTotalsSince_CacheOnlyInputWithAWriteIsAHit(t *testing.T) {
	repo, cleanup := usageAggregateRepo(t)
	defer cleanup()

	// The same shape with a real write is a genuine cache hit and counts.
	seedUsageAggregateRow(t, repo, time.Hour, "openai", "gpt-5.5", 500,
		`{"cached_tokens":500,"cache_creation_input_tokens":500}`)

	hit, total, err := repo.GetUsageCacheHitTotalsSince(time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339))
	if err != nil {
		t.Fatalf("GetUsageCacheHitTotalsSince: %v", err)
	}
	if hit != 500 || total != 1000 {
		t.Errorf("hit = %d, total = %d, want 500/1000", hit, total)
	}
}

func TestGetUsageCacheHitTotalsSince_FallsBackToTheTokensPrompt(t *testing.T) {
	repo, cleanup := usageAggregateRepo(t)
	defer cleanup()

	// The row's own column wins; a zero column defers to the tokens JSON.
	seedUsageAggregateRow(t, repo, time.Hour, "openai", "gpt-5.5", 0,
		`{"prompt_tokens":1000,"cached_tokens":400}`)

	hit, total, err := repo.GetUsageCacheHitTotalsSince(time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339))
	if err != nil {
		t.Fatalf("GetUsageCacheHitTotalsSince: %v", err)
	}
	if hit != 400 || total != 1000 {
		t.Errorf("hit = %d, total = %d, want 400/1000", hit, total)
	}
}

func TestGetUsageTopModelSince_PicksTheHeaviestModel(t *testing.T) {
	repo, cleanup := usageAggregateRepo(t)
	defer cleanup()

	seedUsageAggregateRow(t, repo, time.Hour, "anthropic", "claude-opus-4-6", 1000, `{}`)
	seedUsageAggregateRow(t, repo, 2*time.Hour, "anthropic", "claude-opus-4-6", 500, `{}`)
	seedUsageAggregateRow(t, repo, 3*time.Hour, "openai", "gpt-5.5", 200, `{}`)

	provider, model, tokens, ok, err := repo.GetUsageTopModelSince(time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339))
	if err != nil {
		t.Fatalf("GetUsageTopModelSince: %v", err)
	}
	if !ok {
		t.Fatal("expected a top model for a window with traffic")
	}
	if model != "claude-opus-4-6" || provider != "anthropic" {
		t.Errorf("top model = %s/%s, want anthropic/claude-opus-4-6", provider, model)
	}
	if tokens != 1500 {
		t.Errorf("top model tokens = %d, want 1500", tokens)
	}
}

func TestGetUsageTopModelSince_NoTrafficHasNoTopModel(t *testing.T) {
	repo, cleanup := usageAggregateRepo(t)
	defer cleanup()

	_, _, _, ok, err := repo.GetUsageTopModelSince(time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339))
	if err != nil {
		t.Fatalf("GetUsageTopModelSince: %v", err)
	}
	if ok {
		t.Error("expected no top model for an empty window")
	}
}
