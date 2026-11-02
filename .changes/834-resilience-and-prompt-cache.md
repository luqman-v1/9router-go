### Provider-level admission control and a byte-bounded prompt cache

Two packages ported from `arewedaks/9router-go`, both addressing failures that only show up under real traffic.

**`internal/resilience` — circuit breaker + per-account semaphore.**

The breaker stops hammering a provider that is failing. Four states, not two: CLOSED passes everything, DEGRADED passes but logs (failures are elevated but the threshold has not been reached), OPEN short-circuits until the reset timeout, HALF_OPEN admits a limited number of probes to test recovery.

Two design points are load-bearing:

- **429 is deliberately not counted.** It is per-account rate limiting, so counting it would trip the breaker once a few accounts are individually throttled — blocking every healthy account with them. Only provider-level failures count (408, 500, 502, 503, 504, 520, 524).
- **A probe that never reports must not stall the breaker.** If the caller returns early or the process stalls mid-request, the admitted probe never records an outcome and the breaker would sit in HALF_OPEN forever admitting nothing. The elapsed reset timeout is treated as the probe having failed to prove recovery, and the breaker reopens.

The reset timeout escalates: each `BackoffEscalationCount` failed probe cycles doubles it, capped at `MaxBackoffMultiplier`. A provider that keeps failing on recovery is probed less often rather than every 30 seconds forever. `BreakerStatus` exposes the state, failure count and `retryAfterMs` for the dashboard.

The semaphore caps concurrent requests per `provider:account:proxy` so one account cannot trigger a 429 cascade. A request beyond the cap **waits** for a slot rather than failing, up to a timeout; the queue is bounded so a burst cannot grow without limit. `Block` holds a gate for a duration when an account answers 429, so queued and future requests wait instead of piling on. The proxy hash is part of the key so one dead proxy's limit does not throttle another account sharing it. `Acquire`'s release is idempotent — calling it twice must not over-decrement and hand a phantom slot to a later waiter.

Both are in-memory and process-local. The existing per-connection model locks (`db.LockConnectionModel`) stay: they are per-account and persisted, this layer is per-provider and fast.

**`internal/promptcache` — bounded in bytes, not entries.**

The entry count alone is not a memory bound: with a 512 KiB per-response cap, 1000 entries can hold 512 MB. A production deployment reached **194 MB of retained response bodies — 92% of the process heap — while serving a 0.5% hit rate**. The byte budget is what makes the footprint predictable on the small host this gateway runs on, and `StatsDetail` reports it so an operator can tell a cache holding a few small entries from one holding the process's whole heap.

Three details worth knowing:

- **A per-entry cap above the total budget is clamped.** Otherwise one huge response would evict the whole cache to make room for data unlikely to repeat, and the two limits would contradict each other.
- **Updating an entry releases the size difference.** Replacing a 100-byte body with a 50-byte one must free 50 bytes rather than accumulate, or the budget drifts and evicts live entries.
- **`HashRequest` hashes raw JSON bytes, not a re-encoded map.** Go randomises map iteration and json/v2 does not sort object keys, so decoding into `[]map[string]any` and re-encoding produced two different digests for the *same* input about 15% of the time — the cache then missed on requests that should have hit, silently defeating the feature. Only fields that change the answer participate (`messages`, `system`, `tools`, `temperature`, `top_p`, `max_tokens`, model); `stream` and `user` are ignored so two requests differing only there still share an entry.

The TTL is a real bound, not a filter on reads: with eviction driven only by capacity, an idle server holds every entry until new traffic pushes it out, so a burst of large responses pins memory for as long as nothing else arrives. `Set` sweeps expired entries from the least-recently-used end, bounded by `sweepInterval` so it does not walk the list on every write.

Verified: `go build ./...` clean; `go test ./internal/promptcache/ ./internal/resilience/` pass (34 tests, run repeatedly). New tests pin byte-budget eviction order, update accounting, cap clamping, TTL expiry, breaker state transitions including the unreported-probe reopen and backoff escalation, semaphore queueing/timeout/cancel/block semantics, and hash determinism.
