### Fixed

- **Compression Analytics no longer renders silently truncated breakdowns.** The mode, provider, model and top-savers queries in `queryCompressionAnalyticsTable` ignored the `Scan` error and never read `rows.Err()`, so a row that could not be read mid-iteration ended the loop early and the dashboard received a partial summary with a `nil` error — wrong numbers presented as complete ones. Each loop now returns the query, scan and iteration error, wrapped with the breakdown that failed.

- Pinned by `TestCompressionAnalytics_SummarySurfacesRowErrors`: a non-numeric `tokensSaved` (SQLite keeps it as TEXT despite the `INTEGER` column) makes the top-savers scan fail; before the fix the summary returned `nil` error, after it the failure is surfaced.
