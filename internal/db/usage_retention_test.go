package db

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Retention deletes rows a user can still ask the dashboard about, so the tests
// below pin which rows go and which must survive. The one that must never go is
// usageHistory: it is the billing ledger, and the `all` period reads it
// directly.

func seedDetail(t *testing.T, repo *Repo, id string, at time.Time) {
	t.Helper()
	_, err := repo.RawDB().Exec(
		`INSERT INTO requestDetails (id, timestamp, provider, model, connectionId, status, data)
		 VALUES (?, ?, 'openai', 'gpt-5.5', 'conn-1', 'success', '{"ok":1}')`,
		id, at.UTC().Format("2006-01-02T15:04:05.000Z"))
	if err != nil {
		t.Fatalf("seed requestDetails %s: %v", id, err)
	}
}

func seedLedger(t *testing.T, repo *Repo, id string, at time.Time) {
	t.Helper()
	_, err := repo.RawDB().Exec(
		`INSERT INTO usageHistory (timestamp, provider, model, connectionId, apiKey, endpoint, promptTokens, completionTokens, cost, status, tokens)
		 VALUES (?, 'openai', 'gpt-5.5', 'conn-1', 'sk-a', '/v1/chat/completions', 100, 10, 0.01, 'success', '{}')`,
		at.UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatalf("seed usageHistory: %v", err)
	}
}

// retentionTestRepo returns a repo whose tables come from the real schema
// bootstrap, so these tests exercise the indexes and column types production
// uses rather than a hand-built fixture.
func retentionTestRepo(t *testing.T) *Repo {
	t.Helper()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	if err := EnsureCoreSchema(database); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	return NewRepo(database)
}

func countRows(t *testing.T, repo *Repo, table string) int {
	t.Helper()
	var n int
	if err := repo.RawDB().QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestPruneRequestDetails_KeepsRecentDropsOld(t *testing.T) {
	repo := retentionTestRepo(t)

	now := time.Now().UTC()
	seedDetail(t, repo, "fresh", now.Add(-1*time.Hour))
	seedDetail(t, repo, "edge", now.Add(-29*24*time.Hour))
	seedDetail(t, repo, "stale", now.Add(-31*24*time.Hour))
	seedDetail(t, repo, "ancient", now.Add(-400*24*time.Hour))

	result, err := repo.PruneRequestDetails(context.Background(), 30*24*time.Hour)
	if err != nil {
		t.Fatalf("PruneRequestDetails: %v", err)
	}
	if result.ByAge != 2 {
		t.Errorf("pruned by age = %d, want 2", result.ByAge)
	}
	if got := countRows(t, repo, "requestDetails"); got != 2 {
		t.Errorf("requestDetails rows = %d, want 2", got)
	}

	var remaining []string
	rows, err := repo.RawDB().Query(`SELECT id FROM requestDetails ORDER BY timestamp DESC`)
	if err != nil {
		t.Fatalf("read remaining: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan remaining: %v", err)
		}
		remaining = append(remaining, id)
	}
	if len(remaining) != 2 || remaining[0] != "fresh" || remaining[1] != "edge" {
		t.Errorf("survivors = %v, want [fresh edge]", remaining)
	}
}

// usageHistory is the billing ledger and must survive pruning, whatever its age.
// The `all` usage period reads it directly, so trimming it would silently change
// what the dashboard reports as total lifetime spend.
func TestPruneRequestDetails_NeverTouchesTheLedger(t *testing.T) {
	repo := retentionTestRepo(t)

	now := time.Now().UTC()
	seedLedger(t, repo, "a", now.Add(-400*24*time.Hour))
	seedLedger(t, repo, "b", now.Add(-1*time.Hour))
	seedDetail(t, repo, "stale", now.Add(-400*24*time.Hour))

	if _, err := repo.PruneRequestDetails(context.Background(), 30*24*time.Hour); err != nil {
		t.Fatalf("PruneRequestDetails: %v", err)
	}
	if got := countRows(t, repo, "usageHistory"); got != 2 {
		t.Errorf("usageHistory rows = %d, want 2: pruning must not touch the billing ledger", got)
	}
	if got := countRows(t, repo, "requestDetails"); got != 0 {
		t.Errorf("requestDetails rows = %d, want 0", got)
	}
}

// A gateway under heavy load produces more rows in a month than the ceiling
// allows. The oldest go regardless of age, since the tab pages newest-first and
// would never show them.
func TestPruneRequestDetails_EnforcesRowCeiling(t *testing.T) {
	repo := retentionTestRepo(t)

	// The production ceiling is half a million rows; the pass is exercised at a
	// small one so the test does not spend minutes seeding.
	const ceiling = 500
	prev := retentionMaxRows
	retentionMaxRows = ceiling
	t.Cleanup(func() { retentionMaxRows = prev })

	now := time.Now().UTC()
	// req-0000000 is the newest row; each later id is one second older.
	const over = ceiling + 7
	batch, err := repo.RawDB().Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	stmt, err := batch.Prepare(
		`INSERT INTO requestDetails (id, timestamp, provider, model, connectionId, status, data)
		 VALUES (?, ?, 'openai', 'gpt-5.5', 'conn-1', 'success', '{"ok":1}')`)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	for i := range over {
		if _, err := stmt.Exec(fmt.Sprintf("req-%07d", i),
			now.Add(-time.Duration(i)*time.Second).Format("2006-01-02T15:04:05.000Z")); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	if err := batch.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if got := countRows(t, repo, "requestDetails"); got != over {
		t.Fatalf("seeded %d rows, read back %d", over, got)
	}

	// A year-long retention keeps every row by age, so only the ceiling prunes.
	result, err := repo.PruneRequestDetails(context.Background(), 365*24*time.Hour)
	if err != nil {
		t.Fatalf("PruneRequestDetails: %v", err)
	}
	if result.ByAge != 0 {
		t.Errorf("pruned by age = %d, want 0: a year-long retention must not drop a week-old row", result.ByAge)
	}
	if result.BySize != 7 {
		t.Errorf("pruned by size = %d, want 7", result.BySize)
	}
	if got := countRows(t, repo, "requestDetails"); got != ceiling {
		t.Errorf("requestDetails rows = %d, want %d", got, ceiling)
	}

	// The ceiling keeps the `ceiling` newest rows, which here are
	// req-0000000..req-0000499, and drops the seven oldest. A later id is an
	// older row, so the oldest survivor is the highest id retained.
	wantOldest := fmt.Sprintf("req-%07d", ceiling-1)
	var oldest, newest string
	if err := repo.RawDB().QueryRow(`SELECT id FROM requestDetails ORDER BY timestamp ASC LIMIT 1`).Scan(&oldest); err != nil {
		t.Fatalf("read oldest survivor: %v", err)
	}
	if err := repo.RawDB().QueryRow(`SELECT id FROM requestDetails ORDER BY timestamp DESC LIMIT 1`).Scan(&newest); err != nil {
		t.Fatalf("read newest survivor: %v", err)
	}
	if oldest != wantOldest {
		t.Errorf("oldest survivor = %q, want %q: the ceiling did not drop from the oldest end", oldest, wantOldest)
	}
	if newest != "req-0000000" {
		t.Errorf("newest survivor = %q, want req-0000000: the ceiling dropped a row it should have kept", newest)
	}
}

// A prune run on a table with nothing old must not report rows removed: the loop
// logs what it prunes, and a pass that always claimed work would make the log
// unreadable.
func TestPruneRequestDetails_NothingToDoIsSilent(t *testing.T) {
	repo := retentionTestRepo(t)

	seedDetail(t, repo, "fresh", time.Now().UTC().Add(-time.Hour))

	result, err := repo.PruneRequestDetails(context.Background(), 30*24*time.Hour)
	if err != nil {
		t.Fatalf("PruneRequestDetails: %v", err)
	}
	if result.Total() != 0 {
		t.Errorf("pruned = %d, want 0", result.Total())
	}
	if got := countRows(t, repo, "requestDetails"); got != 1 {
		t.Errorf("requestDetails rows = %d, want 1", got)
	}
}

func TestPruneRequestDetails_RejectsBadInput(t *testing.T) {
	tests := []struct {
		name      string
		retention time.Duration
		ctx       context.Context
	}{
		{name: "zero retention disables nothing", retention: 0, ctx: context.Background()},
		{name: "negative retention", retention: -time.Hour, ctx: context.Background()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database, cleanup := setupTestDB(t)
			defer cleanup()
			repo := NewRepo(database)

			if _, err := repo.PruneRequestDetails(tt.ctx, tt.retention); err == nil {
				t.Fatalf("PruneRequestDetails(retention=%v) = nil error, want a rejection", tt.retention)
			}
		})
	}
}

// A cancelled context means shutdown, not a corrupt pass: the chunk loop must
// stop instead of running a long DELETE to completion.
func TestPruneRequestDetails_CancelledContextStops(t *testing.T) {
	repo := retentionTestRepo(t)

	for i := range 20 {
		seedDetail(t, repo, fmt.Sprintf("req-%d", i), time.Now().UTC().Add(-400*24*time.Hour))
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := repo.PruneRequestDetails(ctx, 30*24*time.Hour); err == nil {
		t.Fatal("PruneRequestDetails with a cancelled context = nil error, want a cancellation")
	}
}
