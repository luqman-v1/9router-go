package db

import (
	"context"
	"log"
	"sync"
	"time"
)

// Retention runs on a timer rather than per request: pruning is a disk-space
// concern, and a request-path prune would put a write transaction in front of
// every inference call.
const (
	retentionInitialDelay = 60 * time.Second
	retentionInterval     = 6 * time.Hour
)

var (
	retentionMu      sync.Mutex
	retentionStarted bool
)

// StartRetentionLoop prunes requestDetails on a timer until ctx is cancelled,
// and prunes usageHistory too when the operator has asked for that (see
// runRetentionPass).
//
// Safe to call more than once; the extra calls are no-ops. Fail-open: a failed
// pass is logged and the next tick retries, because a pruning failure costs disk
// and never costs correctness — requestDetails is diagnostics, and a gateway
// that cannot prune must still serve traffic.
//
// The final pass runs PRAGMA optimize, which refreshes the planner's statistics.
// That matters here because pruning leaves large stretches of freed pages behind
// and leaves indexes describing a table that no longer has the shape they were
// built for.
func StartRetentionLoop(ctx context.Context, repo *Repo, retention time.Duration) {
	if repo == nil {
		return
	}
	if retention <= 0 {
		retention = DefaultRequestDetailRetention
	}

	retentionMu.Lock()
	if retentionStarted {
		retentionMu.Unlock()
		return
	}
	retentionStarted = true
	retentionMu.Unlock()

	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(retentionInitialDelay):
		}

		ticker := time.NewTicker(retentionInterval)
		defer ticker.Stop()

		for {
			runRetentionPass(ctx, repo, retention)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// runRetentionPass runs one tick of the retention loop: requestDetails always,
// the usage ledger only when the operator has asked for it.
//
// Both prunes share a deadline and one PRAGMA optimize, so a pass that removes
// rows leaves the planner's statistics describing the tables it now serves. Keep
// that call here rather than inside either prune: the prune methods are also
// called from the dashboard, and optimising once per table would analyse twice
// for one deletion.
func runRetentionPass(ctx context.Context, repo *Repo, defaultRetention time.Duration) {
	passCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	retention := defaultRetention
	maxRows := 0
	ledgerDays := 0
	if s, err := repo.GetSettings(); err == nil && s != nil {
		if s.RetentionDays > 0 {
			retention = time.Duration(s.RetentionDays) * 24 * time.Hour
		}
		if s.MaxRequestRecords > 0 {
			maxRows = s.MaxRequestRecords
		}
		ledgerDays = s.UsageHistoryRetentionDays
	}

	pruned := pruneRequestDetailRows(passCtx, ctx, repo, retention, maxRows)
	pruned = pruneUsageLedgerRows(passCtx, ctx, repo, ledgerDays) || pruned
	if !pruned {
		return
	}

	if engineOf(repo.RawDB()) == EngineSQLite {
		if _, err := repo.RawDB().ExecContext(passCtx, `PRAGMA optimize`); err != nil {
			if ctx.Err() == nil {
				log.Printf("[RETENTION] PRAGMA optimize failed: %v", err)
			}
		}
	}
}

// pruneRequestDetailRows runs the diagnostics prune and reports whether it
// removed anything. A failure is logged and reported as "no work", because one
// failing table must not stop the pass from reaching the other.
func pruneRequestDetailRows(passCtx, ctx context.Context, repo *Repo, retention time.Duration, maxRows int) bool {
	result, err := repo.PruneRequestDetails(passCtx, retention, maxRows)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("[RETENTION] prune failed: %v", err)
		}
		return false
	}
	if result.Total() == 0 {
		return false
	}
	log.Printf("[RETENTION] pruned %d requestDetails rows (by age: %d, by size: %d)",
		result.Total(), result.ByAge, result.BySize)
	return true
}

// pruneUsageLedgerRows runs the ledger prune when the operator has configured a
// window for it. days <= 0 means the ledger is kept forever, which is the
// default: nothing is read and nothing is deleted.
//
// The count is taken before the delete so the common case — a window that holds
// nothing yet — costs one indexed COUNT and stays silent in the log.
func pruneUsageLedgerRows(passCtx, ctx context.Context, repo *Repo, days int) bool {
	if days <= 0 {
		return false
	}
	retention := time.Duration(days) * 24 * time.Hour
	pending, cutoff, err := repo.CountExpiredUsageHistory(passCtx, retention)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("[RETENTION] usage ledger count failed: %v", err)
		}
		return false
	}
	if pending == 0 {
		return false
	}
	result, err := repo.PruneUsageHistory(passCtx, retention)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("[RETENTION] usage ledger prune failed: %v", err)
		}
		return false
	}
	log.Printf("[RETENTION] pruned %d usageHistory rows before %s (%d tokens carried into per-key budgets)",
		result.Rows, cutoff, result.CarriedTokens)
	return result.Rows > 0
}
