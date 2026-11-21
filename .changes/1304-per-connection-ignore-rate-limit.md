### Per-connection "Ignore rate limit" toggle

Custom OpenAI-compatible endpoints behind an upstream quota window (an hourly
or IP-level rate limit) used to park for the full announced `rateLimitedUntil`
once a 429 arrived, and every request in the window failed with "all in
cooldown" before the first upstream call.

A new operator toggle in Edit Connection (`data.ignoreRateLimit`) shortens
exactly the account-scoped cooldown:

- The selector hands a flagged connection out even while its cooldown is
  active (`internal/handlers/chat/connections.go`), logging the window it is
  riding out, and the session-affinity and pinned-connection paths honour the
  same bypass.
- The failure loops stop writing the park for a flagged connection
  (`comboLockRetryable`, `handleAccountFallback`): the error is still recorded
  for the dashboard, the account stays in rotation, and the next request tries
  it again. Within one failing request the connection is still excluded, so a
  turn never hammers the same exhausted account.
- Deliberately untouched: per-model locks (a dead model fails on every
  connection), the OAuth re-login park, provider node limits, and combo
  Retry-After pass waits.
- `PUT /api/connections/{id}` accepts `ignoreRateLimit` (false removes the
  field), the sanitized payload exposes it, and the edit modal renders the
  checkbox with an explanation.

Verified: db round-trip (`TestConnectionIgnoresRateLimit_RoundTrip`), selector
bypass (`TestGetBestConnection_IgnoreRateLimitBypassesAccountCooldown`), write
side (`TestComboLockRetryable_IgnoreRateLimitSkipsAccountPark`,
`TestComboLockRetryable_FlaggedStaysSelectableNextRequest`), and PUT
persistence (`TestHandleUpdateConnection_IgnoreRateLimitToggle`).
