### Fixed

- **A partially-failed compression-analytics backfill no longer marks itself done.** `BackfillCompressionAnalytics` discarded the `insert` errors and `rows.Err()`, then unconditionally wrote `migration_compression_backfill_done = 'true'`. A backfill that failed partway (full disk, locked database) therefore inserted a partial set of rows and then permanently refused to run again, so the compression dashboard under-reported forever with no error surfaced. The backfill now returns the failure and leaves the marker unwritten, so the next start retries it. The `Prepare`/`Exec` calls also take the request context now.

- Pinned by `TestBackfillCompressionAnalytics_FailureLeavesRetryable`: a trigger that aborts inserts makes the backfill fail; before the fix it returned `nil` and wrote the marker, after it the error is returned, the marker stays unwritten, and a retry succeeds.
