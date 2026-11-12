### feat(resilience): escalating lockout ladder for repeated account failures

Replaces the flat 24h `cooldownBannedAccount` with an escalating lockout
ladder for consecutive account-level failures (e.g. Antigravity TOS bans):

- `GetEscalatingLockoutCooldown(failureStreak)`: streak 1 -> 24h, 2 -> 48h,
  3 -> 96h (4 days), 4 -> 168h (7 days), 5+ -> 336h (14 days); streak 0 or a
  reset -> 0 (no lockout).
- Account-level permanent/severe failures (e.g. `LooksLikeAccountDeactivated`)
  now park using the connection's consecutive failure streak instead of a flat
  24h cooldown, so a dead account is not immediately re-dialled once the first
  window lapses.
- A successful request or a successful test connection clears the streak, so
  the next ban starts from the base cooldown.

Verified with `go vet` and `go test` on `internal/providers` and `internal/db`.
