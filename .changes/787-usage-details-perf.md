### Usage › Details: the dashboard panels no longer stick on "Loading dashboard..."

- The Details section's Activity Heatmap, Node Health and lifetime cards never appeared. The section was not slow: the read answered in single-digit milliseconds, and the component still painted its spinner over the panels until the page was reloaded. Issue reported as "toggle activity heatmap and node health in detail is very slow and does not open".
- Cause: #510 replaced the `onMount` read with an `$effect` and left the success path without clearing `loading`. The `.finally()` guard read `if (!cancelled && !settled) loading = false`, but both `.then()` and `.catch()` set `settled = true` first, so the guard was always false — `data` arrived, and the `{#if loading}` branch kept winning. Both the success and the failure path now clear `loading` themselves, matching the pattern `UsageChart.svelte` already used.
- Every toggle on the section is affected by the same flag: the chevrons opened and closed `heatmapOpen`/`healthOpen` while the body stayed on the spinner, which is what read as "the toggle does not open".
- Verified in a real browser against a running gateway with all panels enabled: the section paints in ~59 ms and each chevron opens its panel in ~10 ms. `web/e2e/usageDetailsPanels.test.ts` pins the rendered DOM — the panel headers and lifetime cards must exist with no spinner left, and the heatmap chevron must collapse and re-open the grid. The test fails on the pre-fix component and passes after it.

### Settings toggles: every switch audited for latency and effect

All 31 non-sidebar switches in Settings › Profile were exercised one by one in a real browser against a running gateway, measuring click-to-paint and confirming the state actually flips:

- **Latency**: median 33 ms, max 45 ms. No toggle exceeds a two-frame budget, and the DOM churn per click is 0–1 nodes and 3–4 attributes, so no switch re-renders a list. `Advanced settings` is the only one that unfolds content (299 ms, 413 → 984 nodes) — it is a disclosure, not a state change.
- **Local switches** (`Unit abbreviation`, `Provider card toggle persistence`, `Advanced settings`, `Expand usage details`, `Show USD next to the converted amount`) write localStorage only, with no round trip. `Unit abbreviation` was verified in both directions against a rendered page: ON shows `1.0M`, OFF shows `1,022,149`.
- **Server switches** (`Require login`, `Force Fallback`, `Enable Observability`, `Enable Global System Prompt`) are form state until Save; the PUT answers in 6 ms and the boot reload that follows costs 5–14 ms per endpoint.
- **Usage panel switches** (20 of them) each flip their own id in `usagePanelLayout`. Verified end-to-end: hiding `Activity Heatmap`, `Node Health` and `Lifetime KPI cards` removes exactly those three from the rendered Details section while the request table stays.
- **Provider and connection switches** were measured too, including the per-card on/off (35 ms to flip, 7 DOM mutations), the per-connection switch (29 ms), and the provider-wide switch (159 ms to flip with 7 accounts). The provider-wide path issues one `PUT` per account plus one boot reload; the live-quota reads it used to be suspected of repeating on every toggle do **not** run there — `loadData()` is keyed on `providerId` and a toggle does not change it, so the 7 paced `/api/usage/{id}` reads happen once when the provider is opened and never again on a toggle.
- `make vet-svelte` unchanged (0 unresolved identifiers, 77 errors against the 77 baseline); `bun test src scripts` 659 pass; `bun test e2e/` 53 pass.

### Usage › Details: the dashboard read runs its queries concurrently

- With the spinner fixed, the Details section still took 310 ms to paint on a 305k-row ledger, and 160 ms of that was `/api/usage/dashboard` itself. The endpoint ran five reads one after another, each a full pass over the trailing week, and the section cannot paint until the last one lands.
- The five reads now run concurrently. Measured on the same data: 145 ms of query time → 84 ms, and the endpoint 160 ms → 59 ms. `go test -race ./internal/handlers/ -run TestHandleUsageDashboard` is clean, and each goroutine writes its own variable before the wait group joins them.
- Two of the five reads were redundant and are now derived from the rows the handler already has. A provider's totals are the sum of its models' totals, and the Top Model card is the largest of those same rows, so `FoldUsageGroupByProvider` (33 µs) and `TopUsageGroupByTokens` replace a 22 ms and a 27 ms `GROUP BY` over the identical window. `internal/db/usage_group_fold_test.go` pins both folds against the SQL they replaced, including the tie-break that keeps the card stable, so the two cannot drift.
- A failed cache-hit read no longer takes the dashboard down: that card falls back to "—" while everything else still renders.

### What is still slow, and why it is not fixed here

- `GET /api/usage/chart?period=all` takes ~850 ms on a 305k-row ledger. The window is the whole table, and the read is a 305k-row scan: measured 470 ms just to stream the rows with no work at all, and 20–50 ms to touch the same row count through a covering index. The trailing-year chart draws only 8,438 (day, model) cells from those 305k rows.
- Grouping that in SQLite instead is **slower**, not faster — measured 784 ms for the `GROUP BY` against 499 ms for the Go fold — because SQLite builds a temp b-tree over the whole window. That change was written, measured and reverted; the numbers are recorded here so it is not attempted again on the assumption that pushing work into SQL is always cheaper.
- The real fix is a day-level rollup of the per-family split (the `usageDaily` blob carries totals only, which is why the chart has to reach into `usageHistory` at all), not a different query over the same rows. It is a schema change with a backfill, so it is left out of a bug-fix PR.
- The `all` preset is the only window affected. `7d` is 30 ms, `30d` 115 ms, and `today` 4 ms.

### Settings toggles: every switch audited for latency and effect

All 31 non-sidebar switches in Settings › Profile were exercised one by one in a real browser against a running gateway, measuring click-to-paint and confirming the state actually flips:

- **Latency**: median 33 ms, max 45 ms. No toggle exceeds a two-frame budget, and the DOM churn per click is 0–1 nodes and 3–4 attributes, so no switch re-renders a list. `Advanced settings` is the only one that unfolds content (299 ms, 413 → 984 nodes) — it is a disclosure, not a state change.
- **Local switches** (`Unit abbreviation`, `Provider card toggle persistence`, `Advanced settings`, `Expand usage details`, `Show USD next to the converted amount`) write localStorage only, with no round trip. `Unit abbreviation` was verified in both directions against a rendered page: ON shows `1.0M`, OFF shows `1,022,149`.
- **Server switches** (`Require login`, `Force Fallback`, `Enable Observability`, `Enable Global System Prompt`) are form state until Save; the PUT answers in 6 ms and the boot reload that follows costs 5–14 ms per endpoint.
- **Usage panel switches** (20 of them) each flip their own id in `usagePanelLayout`. Verified end-to-end: hiding `Activity Heatmap`, `Node Health` and `Lifetime KPI cards` removes exactly those three from the rendered Details section while the request table stays.
- **Provider and connection switches** were measured too, including the per-card on/off (35 ms to flip, 7 DOM mutations), the per-connection switch (29 ms), and the provider-wide switch (159 ms to flip with 7 accounts). The provider-wide path issues one `PUT` per account plus one boot reload; the live-quota reads it was suspected of repeating on every toggle do **not** run there — `loadData()` is keyed on `providerId` and a toggle does not change it, so the 7 paced `/api/usage/{id}` reads happen once when the provider is opened and never again on a toggle. That hypothesis was tested by re-introducing it and measuring zero quota requests either way, and the change was reverted.
- `make vet-svelte` unchanged (0 unresolved identifiers, 77 errors against the 77 baseline); `bun test src scripts` 659 pass; `bun test e2e/` 53 pass.

### Usage page: one dashboard read per visit, and no connection poll where nothing draws it

Two sources of repeated work on the Usage page, both invisible to every check below the browser because the rendered page is identical either way:

- **The Details payload was re-read on every section switch.** `AnalyticsView` renders one section at a time, so leaving Details unmounts `UsageDashboard` and coming back mounts a fresh instance — which re-read the whole payload (five SQL reads, ~120 KB on a busy ledger). Measured three reads across five switches. The payload now lives in a module-level cache (`web/src/lib/usageDashboardCache.ts`) with a 60 s TTL, so a switch back to Details draws immediately. A panel being turned back on drops the entry, because that is the operator explicitly asking for a fresh look. Measured after: **1 read across the same five switches**.
- **The ten-second connection poll ran on every tab.** `App.svelte` re-fetched `/api/connections` (24.5 KB for 46 accounts) and `/api/provider-nodes` (3.5 KB) every ten seconds regardless of which tab was open — including Usage, Quota, Token Saver and Settings, none of which render either list. The poll is now limited to the tabs whose content is drawn from those lists (`endpoint`, `connections`, `combos`, the four media kinds, `proxypools`). A tab that does not draw them still gets a fresh copy on the way in, because `loadData()` runs on navigation.

`web/e2e/usageRequestEconomy.test.ts` pins both as request counts: one dashboard read across five section switches, and zero connection/provider-node reads across two poll ticks on the Usage page. Both assertions fail on the pre-fix code (measured 2 dashboard reads and 4 poll reads in the failing run) and pass after it. `web/src/lib/usageDashboardCache.test.ts` covers the TTL boundary and the invalidation.

### Usage › Overview: the five-second poll no longer carries the breakdown maps

`GET /api/usage/stats` builds five breakdown maps. On a ledger with 411 models the response is **415 KB**, of which the four per-model maps are ~98% — and the Overview polls it every five seconds. Measured **4.9 MB a minute, 1.7 GB an hour**, on a box this project targets at 1 vCPU.

The poll's consumers are the KPI row, the live pulse and the recent-request list: totals, `activeRequests`, `recentRequests`. None of them read `byModel`/`byAccount`/`byApiKey`/`byEndpoint`, and the breakdown table draws exactly one of those at a time.

- `?views=` now selects which breakdowns the handler builds. `views=none` returns totals plus `byProvider` (which the totals are summed from); naming one view builds only that one. Omitting the parameter keeps the previous full shape, so no existing caller changes.
- The dashboard's five-second poll sends `views=none`; the breakdowns are refreshed on a 30 s tick, which keeps the table current without paying the per-poll cost. A poll merges only the fields it fills, so the table keeps its rows between refreshes rather than blanking for four seconds out of five.
- Measured after: **415 KB → 5.1 KB per poll, 4.9 MB/min → 870 KB/min**, with identical totals (1372 requests, same token and cost sums) and the breakdown table still 431 rows across polls.

`internal/handlers/usage_stats_views_test.go` pins the default shape, the totals-only shape (exact totals, empty maps, `byProvider` present), a single named view, and that an unknown name is ignored rather than failing the read. `web/e2e/usageStatsViews.test.ts` pins the wire: every poll inside a 13 s window carries `views=none`, and a full read still lands within 33 s so the table cannot freeze on its first load.
