package db

import (
	"context"
	"fmt"
	"time"
)

// Retention bounds for the diagnostic tables.
//
// requestDetails is pruned. usageHistory is not: it is the billing ledger, the
// `all` usage period reads it directly, and a day can be settled against it
// after the fact. Deleting it to save disk is not a decision this background job
// gets to make on its own.
//
// requestDetails on the other hand only ever feeds the "Recent Request
// Details" tab, which shows the newest page and nothing older. Nothing bills
// from it. At the stored payload size — up to MaxLoggedMessages ×
// MaxMessageContentLen of request text plus MaxResponseContentLen of response
// text, measured at ~20 KB per row — 85k rows is 1.7 GB of diagnostics for a tab
// that renders 20 of them.
const (
	// DefaultRequestDetailRetention is how long a diagnostic row is kept. A
	// month covers the "why did that request three weeks ago fail" question the
	// tab exists to answer, and bounds the table at roughly what a busy gateway
	// produces in that month.
	DefaultRequestDetailRetention = 30 * 24 * time.Hour
	// retentionChunkSize bounds a single DELETE. The statement holds a write
	// lock for its whole run, and a half-million-row delete would block the
	// request path long enough to stall live traffic. Both passes chunk, so
	// neither ever holds the lock for the length of a full-table delete.
	retentionChunkSize = 5000
)

// retentionMaxRows is the hard ceiling on retained rows. A gateway under heavy
// load can produce far more than a month of rows in a month, so age alone would
// leave the table growing without limit. Rows beyond this are dropped
// oldest-first regardless of age, which is the end of the table the Details tab
// pages newest-first through and would never show.
//
// It is a variable rather than a constant so the size pass can be tested
// without seeding half a million rows.
var retentionMaxRows = 500_000

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
func (r *Repo) PruneRequestDetails(ctx context.Context, retention time.Duration) (PruneResult, error) {
	var result PruneResult
	if retention <= 0 {
		return result, fmt.Errorf("prune requestDetails: retention must be positive, got %v", retention)
	}
	if ctx == nil {
		return result, fmt.Errorf("prune requestDetails: nil context")
	}

	if err := r.pruneRequestDetailsByAge(ctx, retention, &result); err != nil {
		return result, err
	}
	if err := r.pruneRequestDetailsBySize(ctx, &result); err != nil {
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
			`DELETE FROM requestDetails WHERE rowid IN (
				SELECT rowid FROM requestDetails WHERE timestamp < ? LIMIT ?
			)`, cutoff, retentionChunkSize)
		if err != nil {
			return fmt.Errorf("delete stale requestDetails: %w", err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("read deleted requestDetails count: %w", err)
		}
		removed += int(affected)
		if affected < retentionChunkSize {
			break
		}
	}

	result.ByAge = removed
	return nil
}

func (r *Repo) pruneRequestDetailsBySize(ctx context.Context, result *PruneResult) error {
	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM requestDetails`).Scan(&total); err != nil {
		return fmt.Errorf("count requestDetails: %w", err)
	}
	if total <= retentionMaxRows {
		return nil
	}

	// Cut from the oldest end, which the timestamp DESC ordering of the tab
	// means are the rows it would never show anyway.
	overflow := total - retentionMaxRows
	removed := 0
	for removed < overflow {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("prune requestDetails by size: %w", err)
		}
		limit := min(retentionChunkSize, overflow-removed)
		res, err := r.db.ExecContext(ctx,
			`DELETE FROM requestDetails WHERE rowid IN (
				SELECT rowid FROM requestDetails ORDER BY timestamp ASC LIMIT ?
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
