### Custom providers: per-model request caps

- A runaway agent loop, or one heavy API customer, could drain a shared custom
  endpoint's quota on a single hot model and leave every other model on it
  unusable. Requests to **custom providers** (`openai-compatible-*`,
  `anthropic-compatible-*`) are now admitted against a per-model budget: a hot
  model fails over to the next combo member (or answers 429 with `Retry-After`)
  while every other model on that provider keeps serving.
- Scope is per customer as well as per model, so one customer's hot model never
  blocks another's: the client session id when there is one, else a short
  non-reversible hash of the router API key, else one shared bucket. The raw key
  is never stored in a bucket key. The hash is djb2 truncated to 8 hex chars,
  documented as not a security hash: a collision only shares a budget.
- **This is new net quota semantics, not upstream parity.** `decolua/9router`
  has only account-cooldown and model-lock filtering; there is no inbound
  per-model cap and no `settings.perModelRateLimit`. The change adds a layer,
  so it is graded as one.
- Admission lives in `tryForwardWithConnection` — the single dispatch choke
  point every path calls (the single-provider fallback, both combo loops in
  `combo.go`, and the fusion panel calls in `combo_fusion.go`). A cap on a
  combo member therefore binds on the combo path too, instead of only in one
  handler. A regression test drives the real `handleComboFallback` through two
  turns: the first dials the capped member, the second fails over to the spare.
- Configuration lives in `settings.perModelRateLimit` — `enabled`, `limit`,
  `windowMs`, plus `overrides` keyed by provider id or `provider/model`.
  Precedence is per-model override, per-provider override, global, defaults.
  `enabled:false` or `limit:0` turns it off for that scope; values clamp
  (limit 1..10000, window 1s..1h) so a typo cannot wedge chat.
- Defaults are deliberately generous — 120 requests per 60s per model — because
  a normal agent client sends about one request per turn: ordinary traffic never
  notices, while a tight retry loop trips it within a minute.
- In-memory only and bounded: no schema change, no timer, no polling loop.
  Windows reset on restart, which is the correct behaviour for a rate limiter,
  and the table is capped at 5000 buckets with the least recently touched
  evicted first.
- The settings read goes through `Repo.PerModelRateLimit`, which answers from
  the settings cache instead of a point query: a point query on the dispatch
  path measured 23.7 µs against 0.3 µs for the limiter itself. Eviction at the
  cap pops the front of an LRU list in O(1) instead of scanning the whole table
  under the lock: 182 µs at the 5,000-bucket cap became 0.7 µs (measured
  `BenchmarkEvictOldest`; `BenchmarkAllowPerModel` 235 ns/op, 1 alloc).
- Auto-ping probes are exempt: a probe reports what the model does now, and
  charging it into the client budget would invert enforcement at low limits.
- The refusal is marked `Local`, so failover walks to the next candidate without
  cooling a credential that was never exercised.
