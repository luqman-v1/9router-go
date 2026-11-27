### Performance

- **The request path reads the settings row once, not six times.** Six request
  helpers each ran their own `SELECT data FROM settings WHERE id = 1` — the
  total-request deadline, the provider thinking modes, the proxy-pool strategy,
  the pinned-connection eligibility check, the connection model filter and the
  usage-detail gate. The row cannot change inside one request, so the repeats
  bought nothing. Both entry points now install a per-request settings holder
  and every reader clones its answer from that one read, exactly as
  `GetSettings` already clones. Measured on a real gateway under streaming load
  (concurrency 50, 20000 requests, 25s CPU profile): the settings row cost
  7.06s of 72.89s (9.69%) before and 4.31s of 71.35s (6.04%) after. A counting
  sqlite driver proves the read count directly — 6 row reads for the six
  readers on `origin/main`, exactly 1 here.
- **The memo is per request, not a process cache.** The holder lives on the
  request context and dies with it, so a settings change the dashboard writes
  between two requests is never served stale, and the DB-03 constraint on
  process-local caches still holds. A context that never went through an entry
  point is unchanged and still reads per call.
- A settings row that cannot be read resolves to nil in every reader, which is
  what each of them already treated as "unset"; the failure shape is pinned by
  `TestSettingsForRequestSurvivesAnUnreadableRow`.

Verification: `go vet ./...` and `go test -count=1 ./...` pass except the
host's SSRF/DNS and stub-bundle failures, reproduced identically on a pristine
`origin/main` worktree. `TestRequestPathHelpersShareOneSettingsRead` asserts the
one-read contract with a counting driver, and
`TestSettingsHolderDoesNotOutliveTheRequest` asserts the next request reads the
row again.
