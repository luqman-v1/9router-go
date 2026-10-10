package db

import (
	"context"
	"testing"
	"time"

	"9router/proxy/internal/analyticsrange"
)

// The window is the contract these analytics report against: every card, every
// breakdown and the trend on one screen have to describe the same period. The
// bug this pins is that only the trend honoured it — GetPromptCacheMetrics
// scanned every usageHistory row regardless of the window the caller asked
// for, so "Last 24 hours" rendered the cache rate of the entire ledger.
//
// The fixture spans 30 days on purpose: a window that quietly stops bounding
// the read still returns the right totals when every row falls inside it.
func TestGetPromptCacheMetrics_HonoursWindow(t *testing.T) {
	repo, cleanup := setupAnalyticsTestDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()

	seed := func(age time.Duration, prompt, cached int) {
		t.Helper()
		ts := now.Add(-age).Format(time.RFC3339)
		if _, err := repo.RawDB().Exec(
			`INSERT INTO usageHistory (timestamp, provider, model, promptTokens, completionTokens, tokens)
			 VALUES (?, 'anthropic', 'claude', ?, 0, ?)`,
			ts, prompt, `{"cached_tokens":`+itoa(cached)+`}`,
		); err != nil {
			t.Fatalf("seed row at %s: %v", age, err)
		}
	}

	seed(2*time.Hour, 1000, 400)  // inside 24h
	seed(6*time.Hour, 1000, 400)  // inside 24h
	seed(48*time.Hour, 1000, 400) // inside 7d, outside 24h
	seed(20*24*time.Hour, 1000, 400)

	day := analyticsrange.Resolve("24h", now)
	if day.Unbounded() {
		t.Fatal("24h must resolve to a bounded window")
	}

	metrics, err := repo.GetPromptCacheMetrics(ctx, day)
	if err != nil {
		t.Fatalf("GetPromptCacheMetrics: %v", err)
	}
	if metrics.TotalRequests != 2 {
		t.Errorf("24h TotalRequests = %d, want 2 (the two rows inside the window)", metrics.TotalRequests)
	}
	if metrics.TotalCachedTokens != 800 {
		t.Errorf("24h TotalCachedTokens = %d, want 800", metrics.TotalCachedTokens)
	}
	if got := metrics.ByProvider["anthropic"].Requests; got != 2 {
		t.Errorf("24h ByProvider[anthropic].Requests = %d, want 2 — the breakdown ignored the window", got)
	}

	week := analyticsrange.Resolve("7d", now)
	weekMetrics, err := repo.GetPromptCacheMetrics(ctx, week)
	if err != nil {
		t.Fatalf("GetPromptCacheMetrics(7d): %v", err)
	}
	if weekMetrics.TotalRequests != 3 {
		t.Errorf("7d TotalRequests = %d, want 3", weekMetrics.TotalRequests)
	}

	all := analyticsrange.Resolve("all", now)
	allMetrics, err := repo.GetPromptCacheMetrics(ctx, all)
	if err != nil {
		t.Fatalf("GetPromptCacheMetrics(all): %v", err)
	}
	if allMetrics.TotalRequests != 4 {
		t.Errorf("all TotalRequests = %d, want 4", allMetrics.TotalRequests)
	}
}

// A trend scoped to a different window than the cards above it is the reading
// the page cannot label away, so the trend takes the same window.
func TestGetPromptCacheTrend_UsesTheGivenWindow(t *testing.T) {
	repo, cleanup := setupAnalyticsTestDB(t)
	defer cleanup()
	now := time.Now().UTC()

	seed := func(age time.Duration) {
		t.Helper()
		if _, err := repo.RawDB().Exec(
			`INSERT INTO usageHistory (timestamp, provider, model, promptTokens, completionTokens, tokens)
			 VALUES (?, 'anthropic', 'claude', 500, 0, '{"cached_tokens":300}')`,
			now.Add(-age).Format(time.RFC3339),
		); err != nil {
			t.Fatalf("seed row at %s: %v", age, err)
		}
	}
	seed(90 * time.Minute)
	seed(48 * time.Hour)

	day, err := repo.GetPromptCacheTrend(context.Background(), analyticsrange.Resolve("24h", now))
	if err != nil {
		t.Fatalf("GetPromptCacheTrend: %v", err)
	}
	var dayRequests int64
	for _, p := range day {
		dayRequests += p.Requests
	}
	if dayRequests != 1 {
		t.Errorf("24h trend covers %d requests, want 1", dayRequests)
	}

	// An unbounded window draws no trend at all: one hourly bucket per hour
	// since the ledger began is not renderable, and a chart of some other
	// period beside all-time cards is two wrong things rather than one.
	all, err := repo.GetPromptCacheTrend(context.Background(), analyticsrange.Resolve("all", now))
	if err != nil {
		t.Fatalf("GetPromptCacheTrend(all): %v", err)
	}
	if len(all) != 0 {
		t.Errorf("all-time trend returned %d buckets, want none", len(all))
	}

	week, err := repo.GetPromptCacheTrend(context.Background(), analyticsrange.Resolve("7d", now))
	if err != nil {
		t.Fatalf("GetPromptCacheTrend(7d): %v", err)
	}
	var weekRequests int64
	for _, p := range week {
		weekRequests += p.Requests
	}
	if weekRequests != 2 {
		t.Errorf("7d trend covers %d requests, want 2", weekRequests)
	}
}

// A ledger that has seen more distinct models than the breakdown cap reports
// how many it left out, so a truncated table is never read as a whole one.
func TestGetPromptCacheMetrics_ReportsTruncation(t *testing.T) {
	repo, cleanup := setupAnalyticsTestDB(t)
	defer cleanup()
	now := time.Now().UTC()

	const models = maxBreakdownGroups + 20
	tx, err := repo.RawDB().Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	for i := range models {
		// Descending cached tokens, so the dropped tail is the least useful.
		ts := now.Add(-time.Duration(i) * time.Minute).Format(time.RFC3339)
		if _, err := tx.Exec(
			`INSERT INTO usageHistory (timestamp, provider, model, promptTokens, completionTokens, tokens)
			 VALUES (?, 'anthropic', ?, 100, 0, '{"cached_tokens":50}')`,
			ts, "model-"+itoa(i),
		); err != nil {
			t.Fatalf("seed model %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	metrics, err := repo.GetPromptCacheMetrics(context.Background(), analyticsrange.Resolve("all", now))
	if err != nil {
		t.Fatalf("GetPromptCacheMetrics: %v", err)
	}
	if len(metrics.ByModel) != maxBreakdownGroups {
		t.Errorf("ByModel has %d entries, want %d", len(metrics.ByModel), maxBreakdownGroups)
	}
	if metrics.TruncatedModels != models-maxBreakdownGroups {
		t.Errorf("TruncatedModels = %d, want %d", metrics.TruncatedModels, models-maxBreakdownGroups)
	}
	// The totals still describe the whole ledger: the cap bounds the table, not
	// the numbers above it.
	if metrics.TotalRequests != models {
		t.Errorf("TotalRequests = %d, want %d — truncation must not shrink the totals", metrics.TotalRequests, models)
	}
	// The top contributor by cached tokens must survive the cut.
	if _, ok := metrics.ByModel["model-0"]; !ok {
		t.Error("model-0 has the largest cached-token total and was dropped from the breakdown")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
