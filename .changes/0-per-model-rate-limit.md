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
  is never stored in a bucket key.
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
- The refusal is marked `Local`, so failover walks to the next candidate without
  cooling a credential that was never exercised.
