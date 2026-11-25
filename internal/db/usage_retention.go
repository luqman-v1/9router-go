package db

import (
	"context"
	"fmt"
	"time"
)

// Retention bounds for the diagnostic tables.
//
// requestDetails is pruned unconditionally: it only ever feeds the "Recent
// Request Details" tab, which shows the newest page and nothing older. Nothing
// bills from it. At the stored payload size — up to MaxLoggedMessages ×
// MaxMessageContentLen of request text plus MaxResponseContentLen of response
// text, measured at ~20 KB per row — 85k rows is 1.7 GB of diagnostics for a tab
// that renders 20 of them.
//
// usageHistory is the billing ledger and is pruned only when an operator asks
// for it: see usage_retention_ledger.go. It is opt-in because the `all` usage
// period and the analytics sections read it directly, so trimming it changes
// what those pages report — and because the ledger is what a per-key tokenQuota
// is measured from, which is why the prune has to carry the spend of the rows it
// removes rather than let them vanish.
const (
	// DefaultRequestDetailRetention is how long a diagnostic row is kept. A
	// month covers the "why did that request three weeks ago fail" question the
	// tab exists to answer, and bounds the table at roughly what a busy gateway
	// produces in that month.
	DefaultRequestDetailRetention = 30 * 24 * time.Hour
)

// retentionChunkSize bounds a single DELETE. The statement holds a write lock
// for its whole run, and a half-million-row delete would block the request path
// long enough to stall live traffic. Every pass chunks, so none of them ever
// holds the lock for the length of a full-table delete.
//
// It is a variable rather than a constant so the chunk loops can be exercised
// against a few hundred rows instead of five thousand (see the ledger tests).
var retentionChunkSize = 5000

// retentionMaxRows is the hard ceiling on retained rows. A gateway under heavy
// load can produce far more than a month of rows in a month, so age alone would
// leave the table growing without limit. Rows beyond this are dropped
// oldest-first regardless of age, which is the end of the table the Details tab
// pages newest-first through and would never show.
//
// It is a variable rather than a constant so the size pass can be tested
// without seeding half a million rows.
var retentionMaxRows = 5000

// PruneResult reports what one retention pass removed.
type PruneResult struct {
	ByAge  int
	BySize int
}

// Total is how many rows the pass dropped.
func (p PruneResult) Total() int { return p.ByAge + p.BySize }

// PruneRequestDetails removes requestDetails rows older than retention, then
// enforces the row ceiling.
//
// It is deliberately not exposed as a user setting. The tab it feeds cannot
// page further back than the retained window, and an operator lowering the
// ceiling to a day would lose diagnostics without being able to see which
// setting did it.
// PruneRequestDetails removes requestDetails rows older than retention, then
// enforces the row ceiling. If maxRows is provided and > 0, it overrides the
// configured or default row ceiling.
func (r *Repo) PruneRequestDetails(ctx context.Context, retention time.Duration, maxRows ...int) (PruneResult, error) {
	var result PruneResult
	if retention <= 0 {
		return result, fmt.Errorf("prune requestDetails: retention must be positive, got %v", retention)
	}
	if ctx == nil {
		return result, fmt.Errorf("prune requestDetails: nil context")
	}

	limit := 0
	if len(maxRows) > 0 {
		limit = maxRows[0]
	}
	if limit <= 0 {
		if s, err := r.GetSettings(); err == nil && s != nil && s.MaxRequestRecords > 0 {
			limit = s.MaxRequestRecords
		} else {
			limit = retentionMaxRows
		}
	}

	if err := r.pruneRequestDetailsByAge(ctx, retention, &result); err != nil {
		return result, err
	}
	if err := r.pruneRequestDetailsBySize(ctx, limit, &result); err != nil {
		return result, err
	}
	return result, nil
}

func (r *Repo) pruneRequestDetailsByAge(ctx context.Context, retention time.Duration, result *PruneResult) error {
	cutoff := time.Now().UTC().Add(-retention).Format("2006-01-02T15:04:05.000Z")

	// Counted first so the pass can stay silent on the common case — nothing is
	// old enough to prune — instead of logging a no-op every interval.
	var stale int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM requestDetails WHERE timestamp < ?`, cutoff).Scan(&stale); err != nil {
		return fmt.Errorf("count stale requestDetails: %w", err)
	}
	if stale == 0 {
		return nil
	}

	removed := 0
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("prune requestDetails by age: %w", err)
		}
		res, err := r.db.ExecContext(ctx,
			`DELETE FROM requestDetails WHERE id IN (
				SELECT id FROM requestDetails WHERE timestamp < ? LIMIT ?
			)`, cutoff, retentionChunkSize)
		if err != nil {
			return fmt.Errorf("delete stale requestDetails: %w", err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("read deleted requestDetails count: %w", err)
		}
		removed += int(affected)
		if affected < int64(retentionChunkSize) {
			break
		}
	}

	result.ByAge = removed
	return nil
}

func (r *Repo) pruneRequestDetailsBySize(ctx context.Context, maxRows int, result *PruneResult) error {
	if maxRows <= 0 {
		maxRows = retentionMaxRows
	}
	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM requestDetails`).Scan(&total); err != nil {
		return fmt.Errorf("count requestDetails: %w", err)
	}
	if total <= maxRows {
		return nil
	}

	// Cut from the oldest end, which the timestamp DESC ordering of the tab
	// means are the rows it would never show anyway.
	overflow := total - maxRows
	removed := 0
	for removed < overflow {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("prune requestDetails by size: %w", err)
		}
		limit := min(retentionChunkSize, overflow-removed)
		res, err := r.db.ExecContext(ctx,
			`DELETE FROM requestDetails WHERE id IN (
				SELECT id FROM requestDetails ORDER BY timestamp ASC LIMIT ?
			)`, limit)
		if err != nil {
			return fmt.Errorf("delete excess requestDetails: %w", err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("read deleted requestDetails count: %w", err)
		}
		if affected == 0 {
			break
		}
		removed += int(affected)
	}

	result.BySize = removed
	return nil
}

// DatabaseStats holds metrics for database disk usage and key table volumes.
type DatabaseStats struct {
	DatabaseSizeBytes   int64 `json:"databaseSizeBytes"`
	ReclaimableBytes    int64 `json:"reclaimableBytes"`
	RequestDetailsCount int   `json:"requestDetailsCount"`
	UsageHistoryCount   int   `json:"usageHistoryCount"`
	// UsageHistoryOldestTimestamp is the timestamp of the oldest retained ledger
	// row, or empty when there are none. It is what tells an operator how far back
	// the usage pages actually reach, and it is the number a ledger prune moves —
	// without it, "did that prune do anything" is unanswerable from the dashboard.
	//
	// Both bounds ride idx_uh_ts as covering-index seeks (MIN/MAX over the indexed
	// column), so this stays O(log n) on a ledger of any size.
	UsageHistoryOldestTimestamp string `json:"usageHistoryOldestTimestamp"`
	UsageHistoryNewestTimestamp string `json:"usageHistoryNewestTimestamp"`
}

// GetDatabaseStats queries SQLite page counts and table row counts to measure
// disk consumption and reclaimable space.
func (r *Repo) GetDatabaseStats(ctx context.Context) (DatabaseStats, error) {
	var stats DatabaseStats
	if r == nil || r.db == nil {
		return stats, fmt.Errorf("repo or db is nil")
	}

	// Size stats are SQLite-specific (page/file metrics). Postgres exposes
	// size via pg_database_size; until that path exists, skip the sizing block
	// and report only row counts.
	if engineOf(r.db) == EngineSQLite {
		var pageCount, pageSize, freelistCount int64
		if err := r.db.QueryRowContext(ctx, "PRAGMA page_count;").Scan(&pageCount); err != nil {
			return stats, fmt.Errorf("read page_count: %w", err)
		}
		if err := r.db.QueryRowContext(ctx, "PRAGMA page_size;").Scan(&pageSize); err != nil {
			return stats, fmt.Errorf("read page_size: %w", err)
		}
		if err := r.db.QueryRowContext(ctx, "PRAGMA freelist_count;").Scan(&freelistCount); err != nil {
			return stats, fmt.Errorf("read freelist_count: %w", err)
		}
		stats.DatabaseSizeBytes = pageCount * pageSize
		stats.ReclaimableBytes = freelistCount * pageSize
	}

	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM requestDetails;").Scan(&stats.RequestDetailsCount); err != nil {
		return stats, fmt.Errorf("count requestDetails: %w", err)
	}
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM usageHistory;").Scan(&stats.UsageHistoryCount); err != nil {
		return stats, fmt.Errorf("count usageHistory: %w", err)
	}
	if err := r.db.QueryRowContext(ctx,
		`SELECT COALESCE(MIN(timestamp), ''), COALESCE(MAX(timestamp), '') FROM usageHistory`,
	).Scan(&stats.UsageHistoryOldestTimestamp, &stats.UsageHistoryNewestTimestamp); err != nil {
		return stats, fmt.Errorf("read usageHistory bounds: %w", err)
	}

	return stats, nil
}

// VacuumDatabase executes SQLite VACUUM with a bounded timeout to reclaim free
// pages and return allocated storage to the operating system. On Postgres the
// equivalent is VACUUM (no timeout wrapper needed), so dispatch by engine.
func (r *Repo) VacuumDatabase(ctx context.Context) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("repo or db is nil")
	}
	vCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if _, err := r.db.ExecContext(vCtx, "VACUUM;"); err != nil {
		return fmt.Errorf("vacuum database: %w", err)
	}
	return nil
}
