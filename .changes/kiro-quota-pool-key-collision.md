### fix(kiro): keep both credit pools apart in the quota view
- A `KIRO POOLING` seat answers `getUsageLimits` with **two** rows both carrying `resourceType: "CREDIT"`: a `NON_POOLED` row that has usage but no limit, and the `POOLED` row that carries the shared pool. Both were keyed by `resourceType`, so the later row overwrote the earlier and the dashboard showed whichever AWS happened to send last.
- When the limit-less row came last, the panel said `total 0, remaining -0.59` — a pool reading as over-drawn that was never granted at all, which is what made a healthy 84k-credit seat look broken.
- `dimensionType` now always joins the key (`credit_pooled`, `credit_non_pooled`), so the classification no longer depends on row order.
- A row with no `usageLimitWithPrecision` is reported as `unlimited` with `remaining: 0` instead of `total - used` going negative.
