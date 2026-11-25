### Usage: a month-to-date period

- **Ported from `giienew/9router-pro@v0.5.109-pro.29`** — `src/app/(dashboard)/dashboard/usage/page.js`
  (`{ value: "month", label: "Month" }`) and `src/app/api/usage/latency/route.js`
  (`monthStartIso`, "Month-to-date: details since local midnight on the 1st of
  this month"). This is **Pro-only, not upstream parity**: `decolua/9router` has
  no month period, only an unrelated `timeRange === "month"` in code search.
### Usage: a month-to-date period

- The period picker gains **This month**, resolved as month-to-date — the
  window opens at local midnight on the 1st — rather than as a rolling 30 days.
  On the 10th that makes it narrower than `30d`, which is the point: "this
  month" and "last 30 days" are different questions.
- The 1st is the boundary a naive "truncate to the day" gets wrong: at 00:30 the
  month opened 30 minutes ago, not 24 hours. The resolver uses an explicit date,
  not a day floor.
- Wired through every surface that reads a period, so the cards, the chart and
  the request-details table describe one window: `resolveUsagePeriod` (stats,
  details), `parseChartPeriod` (one daily bucket per day elapsed in the month)
  and `analyticsrange.Resolve` (cache, compression and per-key analytics).
  The chart gets its own kind because a day count of `0` already means "from
  the earliest row" — the `all` preset.
- The month window is anchored like `today`, so every poll in the month shares
  one aggregate-cache entry instead of missing on each request.
