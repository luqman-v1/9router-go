### Fixed

- Combo failover (round-robin / fallback) no longer stalls on a failing
  member, especially in combos whose members all share one connection
  (an aggregator node with a single API key):

  - **500 / 501 / 408 / 404 and other 5xx now advance the combo.**
    `providers.IsMemberFailoverStatus` widens `RetryableStatusCodes` with
    the member-side statuses the account table omits. The OpenAI and Claude
    combo loops break to the next member after ONE attempt on such a
    status instead of re-picking the same connection up to 10 times per
    member (up to 80 doomed upstream calls for an 8-member combo), which
    read as the rotation freezing on the failing model. A per-entry
    `triedConns` guard makes a second dial of the same connection for the
    same member impossible for every other status as well.
  - **A bare 401 no longer parks the whole shared account.**
    `comboLockRetryable` wrote an account-wide `LockConnectionRateLimit`
    (120 s for a 401) and excluded the connection for the rest of the pass
    for every non-model-scoped retryable error. On a single-connection
    combo every later member then died in the connection selector. The
    account lock and the pass exclusion now require a demonstrably
    account-scoped failure (`isAccountScopedFailure`: a 429, or an
    auth-failure text naming the credential). Anything else records a
    scoped error and locks only the tried model on that connection, so
    the remaining members keep their one attempt each.
  - **No more silent members.** The combo loops' `getBestConnection`
    failure path used to `break` without logging; it now logs a
    `[combo]` warning naming provider, model, entry and the selector
    error, and seeds `lastErr` so the client is not told "no valid
    entries" when every member was starved. The new member-failover
    advance is likewise logged (`member failed, advancing to next
    member`).
  - `handleAccountFallback` matches: a member-side status (500/501/408/
    404/other 5xx) on one connection now falls through to the next
    connection instead of returning the first connection's error.

### Verified

- New regression test `combo_failover_shared_account_test.go`: one shared
  connection, first combo member failing, second healthy. On the pre-fix
  code all three cases fail with the reported signatures (501 and 500
  attempted the failing member 10 times; a generic 401 was returned to
  the client and the second member never served). With the fix each
  failing member is attempted exactly once and the second member answers
  200.
- `go test ./internal/handlers/chat/ ./internal/providers/ ./internal/db/`
  — all pass.
- Binary rebuilt (`1.9.14-exp.4`, `-s -w`) and smoke-tested on port
  20199: `/health` ok, `/api/version` reports the build.
