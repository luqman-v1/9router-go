package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestRollupUsageHistoryBeforeDate_AggregatesAndPersists tests that rows older
// than cutoff are rolled up into usageDaily before deletion, and persist after deletion.
func TestRollupUsageHistoryBeforeDate_AggregatesAndPersists(t *testing.T) {
	repo := retentionTestRepo(t)
	ctx := context.Background()

	// Seed 3 rows across 2 dates in the past (e.g. 40 days ago and 35 days ago)
	date1 := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	date2 := time.Date(2026, 1, 16, 14, 0, 0, 0, time.UTC)
	recentDate := time.Now().UTC().Add(-2 * time.Hour)

	// Row 1 on date1: openai, gpt-4o, 100 prompt, 50 compl, 0.002 cost
	seedLedgerRowFull(t, repo, 1, "sk-test", "openai", "gpt-4o", "conn-1", "/v1/chat/completions",
		100, 50, 0.002, "success", `{"prompt_tokens":100,"completion_tokens":50,"cached_tokens":20}`, date1)
	// Row 2 on date1: anthropic, claude-3-5-sonnet, 200 prompt, 100 compl, 0.005 cost
	seedLedgerRowFull(t, repo, 2, "sk-test", "anthropic", "claude-3-5-sonnet", "conn-2", "/v1/chat/completions",
		200, 100, 0.005, "success", `{"prompt_tokens":200,"completion_tokens":100,"cached_tokens":0}`, date1)
	// Row 3 on date2: openai, gpt-4o, 300 prompt, 150 compl, 0.006 cost
	seedLedgerRowFull(t, repo, 3, "sk-test", "openai", "gpt-4o", "conn-1", "/v1/chat/completions",
		300, 150, 0.006, "success", `{"prompt_tokens":300,"completion_tokens":150,"cached_tokens":50}`, date2)
	// Row 4 recent (should NOT be deleted by a 30-day cutoff)
	seedLedgerRowFull(t, repo, 4, "sk-test", "openai", "gpt-4o", "conn-1", "/v1/chat/completions",
		50, 25, 0.001, "success", `{}`, recentDate)

	// Ensure no daily usage exists prior to prune
	if _, err := repo.GetUsageDaily("2026-01-15"); err == nil {
		t.Fatal("expected no daily usage for 2026-01-15 before prune")
	}

	// 1. Prune usage history using retention duration (30 days).
	// PruneUsageHistory calls RollupUsageHistoryBeforeDate before deleting raw rows.
	res, err := repo.PruneUsageHistory(ctx, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("PruneUsageHistory failed: %v", err)
	}
	if res.Rows != 3 {
		t.Errorf("pruned rows = %d, want 3", res.Rows)
	}

	// 2. Verify summary rows persist even after raw usageHistory rows are deleted
	d1Raw, err := repo.GetUsageDaily("2026-01-15")
	if err != nil {
		t.Fatalf("GetUsageDaily 2026-01-15: %v", err)
	}
	var d1 map[string]any
	if err := json.Unmarshal([]byte(d1Raw), &d1); err != nil {
		t.Fatalf("unmarshal d1: %v", err)
	}
	if reqs, ok := d1["requests"].(float64); !ok || int(reqs) != 2 {
		t.Errorf("date1 requests = %v, want 2", d1["requests"])
	}
	if pt, ok := d1["promptTokens"].(float64); !ok || int(pt) != 300 {
		t.Errorf("date1 promptTokens = %v, want 300", d1["promptTokens"])
	}
	if ct, ok := d1["completionTokens"].(float64); !ok || int(ct) != 150 {
		t.Errorf("date1 completionTokens = %v, want 150", d1["completionTokens"])
	}
	if cached, ok := d1["cachedTokens"].(float64); !ok || int(cached) != 20 {
		t.Errorf("date1 cachedTokens = %v, want 20", d1["cachedTokens"])
	}

	d2Raw, err := repo.GetUsageDaily("2026-01-16")
	if err != nil {
		t.Fatalf("GetUsageDaily 2026-01-16: %v", err)
	}
	var d2 map[string]any
	if err := json.Unmarshal([]byte(d2Raw), &d2); err != nil {
		t.Fatalf("unmarshal d2: %v", err)
	}
	if reqs, ok := d2["requests"].(float64); !ok || int(reqs) != 1 {
		t.Errorf("date2 requests = %v, want 1", d2["requests"])
	}
	if pt, ok := d2["promptTokens"].(float64); !ok || int(pt) != 300 {
		t.Errorf("date2 promptTokens = %v, want 300", d2["promptTokens"])
	}

	// Verify only row 4 survived in usageHistory
	if got := countRows(t, repo, "usageHistory"); got != 1 {
		t.Errorf("surviving usageHistory rows = %d, want 1", got)
	}
}

// seedLedgerRowFull inserts one usageHistory row with exact details.
func seedLedgerRowFull(t *testing.T, repo *Repo, id int64, apiKey, provider, model, connID, endpoint string, prompt, compl int, cost float64, status, tokensJSON string, at time.Time) {
	t.Helper()
	ts := at.UTC().Truncate(time.Second).Format(time.RFC3339)
	q := `INSERT INTO usageHistory (id, timestamp, provider, model, connectionId, apiKey, clientKey, endpoint, promptTokens, completionTokens, cost, status, tokens, meta)
	     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '')`
	if _, err := repo.RawDB().Exec(q, id, ts, provider, model, connID, apiKey, apiKey, endpoint, prompt, compl, cost, status, tokensJSON); err != nil {
		t.Fatalf("seed usageHistory full: %v", err)
	}
}

// TestRollupUsageHistoryBeforeDate_DoesNotDoubleExistingDaily asserts that the
// rollup pass does not re-aggregate raw usageHistory rows into a date whose
// usageDaily summary already exists (written live by the request path), so a
// retention prune cannot double-count tokens and requests for existing days.
func TestRollupUsageHistoryBeforeDate_DoesNotDoubleExistingDaily(t *testing.T) {
	t.Parallel()
	repo := retentionTestRepo(t)
	ctx := context.Background()

	date1 := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	date2 := time.Date(2026, 1, 16, 14, 0, 0, 0, time.UTC)

	// The request path already wrote this date's daily summary live; the rollup
	// must not touch it again.
	if err := repo.UpsertUsageDaily("2026-01-15", `{"requests":5,"promptTokens":100,"completionTokens":40,"cachedTokens":10,"cost":0.01}`); err != nil {
		t.Fatalf("UpsertUsageDaily: %v", err)
	}

	// Two raw rows on date1 (must be ignored: summary already exists),
	// one raw row on date2 (no summary yet: must be backfilled).
	seedLedgerRowFull(t, repo, 1, "sk-test", "openai", "gpt-4o", "conn-1", "/v1/chat/completions",
		100, 50, 0.002, "success", `{"prompt_tokens":100,"completion_tokens":50,"cached_tokens":20}`, date1)
	seedLedgerRowFull(t, repo, 2, "sk-test", "anthropic", "claude-3-5-sonnet", "conn-2", "/v1/chat/completions",
		200, 100, 0.005, "success", `{"prompt_tokens":200,"completion_tokens":100,"cached_tokens":0}`, date1)
	seedLedgerRowFull(t, repo, 3, "sk-test", "openai", "gpt-4o", "conn-1", "/v1/chat/completions",
		300, 150, 0.006, "success", `{"prompt_tokens":300,"completion_tokens":150,"cached_tokens":50}`, date2)

	res, err := repo.PruneUsageHistory(ctx, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("PruneUsageHistory failed: %v", err)
	}
	if res.Rows != 3 {
		t.Errorf("pruned rows = %d, want 3", res.Rows)
	}

	// date1 summary must be exactly as it was before prune — not doubled.
	d1Raw, err := repo.GetUsageDaily("2026-01-15")
	if err != nil {
		t.Fatalf("GetUsageDaily 2026-01-15: %v", err)
	}
	var d1 map[string]any
	if err := json.Unmarshal([]byte(d1Raw), &d1); err != nil {
		t.Fatalf("unmarshal d1: %v", err)
	}
	if reqs, ok := d1["requests"].(float64); !ok || int(reqs) != 5 {
		t.Errorf("date1 requests = %v, want 5 (not doubled by rollup)", d1["requests"])
	}
	if pt, ok := d1["promptTokens"].(float64); !ok || int(pt) != 100 {
		t.Errorf("date1 promptTokens = %v, want 100 (not doubled by rollup)", d1["promptTokens"])
	}

	// date2 had no prior summary, so it is backfilled from raw rows.
	d2Raw, err := repo.GetUsageDaily("2026-01-16")
	if err != nil {
		t.Fatalf("GetUsageDaily 2026-01-16: %v", err)
	}
	var d2 map[string]any
	if err := json.Unmarshal([]byte(d2Raw), &d2); err != nil {
		t.Fatalf("unmarshal d2: %v", err)
	}
	if reqs, ok := d2["requests"].(float64); !ok || int(reqs) != 1 {
		t.Errorf("date2 requests = %v, want 1", d2["requests"])
	}
	if pt, ok := d2["promptTokens"].(float64); !ok || int(pt) != 300 {
		t.Errorf("date2 promptTokens = %v, want 300", d2["promptTokens"])
	}

	if got := countRows(t, repo, "usageHistory"); got != 0 {
		t.Errorf("surviving usageHistory rows = %d, want 0", got)
	}
}
