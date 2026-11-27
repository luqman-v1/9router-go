### Fixed

- **Compression Analytics and the semantic cache no longer swallow row-iteration errors.** The row loops that #1510 did not reach ignored the `Scan` error and never read `rows.Err()`, so a row that could not be read mid-iteration ended the loop early and the caller received a partial result with a `nil` error. The compression-analytics file was the one file in the tree whose loops outnumbered their `rows.Err()` checks (11 vs 6); it is now 11 vs 11, like the rest of `internal/db`.

  - `queryCompressionAnalyticsTable`: the hourly-trend and savings-by-provider/model loops.
  - `queryUsageHistoryBackfill`: the provider, hourly-trend and top-savers loops.

- **The semantic cache reports a failed hydration at boot.** `PersistentLRUStore.Hydrate` returned `nil` when a row could not be scanned, so the gateway booted with however much of the cache had loaded and no indication that the rest was never read — a cache that simply misses a lot, with nothing in the log to say why. `NewPersistentStore` also discarded that error outright (`_ =`), so nothing surfaced it at all. Hydrate now returns the scan and iteration error, and the constructor logs `hydrate from disk failed; starting cold` with the cause. The cache still starts: a warm cache is an optimisation, not a precondition for routing.

  A missing table is not reported, because the store is built against a repository before its schema is ensured in some paths and a fresh install has no table yet — the same condition `Put` and `Delete` already tolerate. Existence is asked of the engine catalog (`sqlite_master` / `information_schema`) rather than inferred from the driver's message, since an error string is not a contract.

- Pinned by `TestPersistentLRUStore_HydrateSurfacesScanError`: a non-numeric `hitCount` (SQLite keeps it as TEXT despite the `INTEGER` column) fails the scan mid-loop. Before the fix `Hydrate` returned `nil`; after it the failure is surfaced.
