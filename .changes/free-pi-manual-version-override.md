### fix(free-pi): let a manual `x-client-version` override win

An operator who sets `x-client-version` in the dashboard's provider header
overrides expects that value to be the final word. Today the free-pi executor
still resolves the version from the live `/client-version` feed on every
request; the override only won because the executor happens to copy
`StaticHeaders` over its own map last. That left two inconsistencies: the log
reported the resolved value rather than the one actually sent, and a 426 could
trigger a "self-heal" retry that replayed the operator's header unchanged.

- A configured `x-client-version` (any RFC 7230 spelling, matched
  case-insensitively) is now detected up front and used directly as
  `state.ClientVersion`; the automatic resolver is skipped entirely.
- A 426 with a manual override is reported immediately instead of spending a
  retry that cannot change the header — the operator owns that decision.
- No override configured: behaviour is unchanged, the live-feed resolver still
  lifts stale pins and self-heals 426 as before.

Covered by three table/flow tests: manual override wins over a newer published
`latest` for both lower- and mixed-case names, no-override keeps automatic
resolution, and a 426 with an override is surfaced in one attempt. `go test`
for `internal/proxy/executor` passes, `gofmt` clean.
