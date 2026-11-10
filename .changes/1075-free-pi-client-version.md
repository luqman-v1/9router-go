### fix(free-pi): resolve `x-client-version` against the live floor so a stale login pin stops answering 426

Every request answered `HTTP 426: your free-pi-cli is below the required
minimum (0.6.16)` from a container that was already current.

`x-client-version` is free-pi's forced-update gate: `GET /client-version`
publishes a `min` floor and refuses anything older. The value sent was
`cv.Latest` captured **once**, at login, and persisted onto the connection
(`saveFreePiConnection` → `providerSpecific["freePiClientVersion"]`).
`freePiClientVersionFor` then returned that stored string unconditionally —
nothing refreshed it, and nothing compared it with the published `min`. When
the server raised the floor past a snapshot taken earlier, the pin kept
winning, and raising the compiled-in fallback could not help because a
non-empty stored value outranks the constant.

- **Resolve per request.** `ResolveFreePiClientVersion` sends the highest of the
  stored snapshot, the server's published `latest`, and the compiled-in floor,
  and never below the published `min`. A stale pin can no longer beat a current
  server value, so existing connections heal on their first request after
  upgrading — no re-login.
- **Cache the snapshot behind a 15-minute TTL** with at most one fetch in
  flight, so a burst past the TTL reuses the last snapshot rather than queueing
  on the mutex or stampeding the endpoint. One snapshot, one flag, no map, no
  goroutine (§4.G). A failed refresh keeps the previous snapshot and never fails
  the request.
- **Heal a 426 once.** A server-side raise inside the TTL is the case the cache
  cannot predict, so a 426 forces a refresh, re-resolves the header, and retries
  exactly once — mirroring the existing 409 `concurrent_session` self-heal. The
  retry is skipped when the published floor did not advance, because replaying
  an unchanged header only draws the same answer.
- **Local comparator.** `internal/updater` owns this repo's release-tag
  semantics (pre-release precedence, build metadata); this compares a vendor's
  plain dotted numbers, where `0.7` must beat `0.6.16` — which a naive string
  comparison gets backwards.

Covered by 13 table-driven cases in `freepi_version_test.go` (comparator
ordering, stale-pin lifting, `min` outranking a lower `latest`, TTL caching, a
failed refresh falling back to the highest value known) and 3 executor cases in
`free_pi_version_test.go` (426 recovers with exactly one retry; a known floor
prevents the 426 entirely; a non-advancing floor does not spend a retry).
`golangci-lint --new-from-rev` reports 0 issues and `gofmt` is clean.
