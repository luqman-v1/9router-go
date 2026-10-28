### Fixed: Usage page — hidden panels no longer fetch or show loading cards (issue #506)

**Frontend**
- `UsageDashboard.svelte`: The Details payload is now read only while at least one of its cards (`details.lifetime`, `details.heatmap`, `details.nodeHealth`, `details.requests`, `details.payload`) is visible. Hiding every card stops the fetch and its loading card; re-enabling one starts it again. The read used to run once on mount, so a section whose cards were all hidden still fetched the payload and painted a loading card over a section that draws nothing.
- `AnalyticsView.svelte`: The Overview stats read and the live SSE stream are now gated by `overviewVisible` (the section has at least one visible panel). The Details paged request table read is gated by `isPanelVisible('details.requests')`.
- `UsageChart.svelte`: The chart fetch is bounded to 20 s. A read that never answers leaves the panel spinning with no way out; the wait is now bounded so the chart says it could not be loaded, and the reader can collapse and expand the panel to retry.
- `UsageDashboard.svelte`: The Details payload fetch is bounded to 20 s with the same rationale.

**Backend**
- `db/usage.go`: Added `GetUsageHistoryChartSince` (narrow projection: timestamp, model, promptTokens, completionTokens, cost — no tokens JSON) and `GetUsageTopModelSince` / `GetUsageCacheHitTotalsSince` (SQL aggregates for the Details lifetime cards).
- `internal/handlers/usage_chart.go`: Uses the narrow chart projection instead of the wide `GetUsageHistorySince`.
- `internal/handlers/usage_dashboard.go`: Uses the SQL aggregates for Top Model and cache-hit rate instead of folding every row of the window in the process. The row-level `pickTopModel` and `windowCacheHitRate` folds are removed.
- `db/schema.go`: Added covering index `idx_uh_analytics` on `usageHistory(timestamp, provider, model, status, promptTokens, completionTokens, cost)` so the three heaviest reads (chart, group-by-provider, group-by-model) are answered from the index alone — no row of the window is read.

**Performance (measured on a 1.2 M-row ledger, 7-day window)**
- `GET /api/usage/chart?period=7d`: 6.2 s → 1.3 s (4.8×)
- `GET /api/usage/dashboard`: 19.4 s → 10.2 s (1.9×)
- `GetUsageGroupByProviderSince`: 5.8 s → 1.1 s (5.3×)
- `GetUsageGroupByModelSince`: 6.4 s → 1.8 s (3.5×)
- `GetRequestDetailPerf`: unchanged at ~35 ms (already narrow)

**Tests**
- New `internal/db/usage_aggregates_test.go` pins the SQL counting rules for cache-hit totals (cache-inclusive, cache-exclusive, uncached, cache-only import, fallback to tokens JSON) and top-model selection.
- Obsolete `TestWindowCacheHitRate*` tests in `internal/handlers/usage_dashboard_test.go` removed.
