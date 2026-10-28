### Test Fix

- Fix `TestHandleUsageChart/daily_window_returns_buckets_with_totals_and_family_split` flakiness caused by wall-clock drift.
- `setupChartTestDB` now seeds `usageDaily` with yesterday's date (dynamic) instead of the hardcoded `2026-10-16`.
- `TestDailyChartBucketsAll` updated to use `time.Now().UTC()` so it covers both seeded days (yesterday's daily rollup + today's history row).
- Verified: `go test -v -count=1 ./internal/handlers/ -run 'TestHandleUsageChart|TestDailyChartBucketsAll'` passes on any date; the same run on the pre-fix parent fails with `chart total = 0, want at least the seeded 1500`.
