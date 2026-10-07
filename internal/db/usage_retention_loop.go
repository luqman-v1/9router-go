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

// StartRetentionLoop prunes requestDetails on a timer until ctx is cancelled.
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

func runRetentionPass(ctx context.Context, repo *Repo, retention time.Duration) {
	passCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	result, err := repo.PruneRequestDetails(passCtx, retention)
	if err != nil {
		if ctx.Err() != nil {
			// Shutting down mid-pass is not a failure.
			return
		}
		log.Printf("[RETENTION] prune failed: %v", err)
		return
	}
	if result.Total() > 0 {
		log.Printf("[RETENTION] pruned %d requestDetails rows (by age: %d, by size: %d)",
			result.Total(), result.ByAge, result.BySize)
	}

	if _, err := repo.RawDB().ExecContext(passCtx, `PRAGMA optimize`); err != nil {
		if ctx.Err() == nil {
			log.Printf("[RETENTION] PRAGMA optimize failed: %v", err)
		}
	}
}
