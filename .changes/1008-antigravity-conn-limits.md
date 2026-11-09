### feat(antigravity): bound per-connection concurrency, backoff, circuit breaker

- Adds `internal/connlimit`: a per-connection admission controller keyed by
  connection ID (not per provider, not global). It caps in-flight upstream
  requests at a default of 3 per Antigravity connection (`DefaultConcurrency`).
  Over the limit the request fails fast with a labelled error naming the limit
  (`ErrConnLimit`) — never unbounded queueing, no goroutine per request.
- On repeated 403/429 from one connection, the next attempt widens an
  exponential, jittered backoff (2s, 4s, 8s … capped at 1 minute). It resets
  only on a successful upstream response — never on a token refresh. A 403
  `TOS_VIOLATION` bypasses backoff and breaker entirely, matching #996's
  permanent handling (regression-pinned by test).
- Per-connection circuit breaker: 3 consecutive failures open it; fail fast
  locally with zero upstream calls until the 30s cool-down, then exactly one
  half-open probe. Released on connection delete and on re-authentication.
- Bounded state: the registry is capped at 10,000 connections and evicts the
  least-recently-active entry, so it cannot grow without bound on
  client-supplied IDs. Deleting a connection drops its breaker/backoff entry
  (dashboard `DELETE /api/connections/{id}` now calls `connlimit.Close`), and
  re-authentication clears it too.
- Wired into the Antigravity dispatch path (`forwardGeminiNativeRequest`):
  `connlimit.Allow` then `connlimit.Acquire` before every upstream call, with
  `RecordSuccess`/`RecordFailure` charged on completion.
- Tests: concurrency bound (at most N upstream calls for N+1 concurrent
  requests), progressive backoff delay computed without sleeping, TOS_VIOLATION
  bypass, open-breaker → zero calls → exactly one probe after cool-down,
  delete evicts state (map shrinks), goroutine-leak assertion.
