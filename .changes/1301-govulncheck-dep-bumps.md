### Changed

- **chore(deps): bump `golang.org/x/net` to v0.62.0 and `golang.org/x/crypto` to v0.59.0** — clears the three dependency-level findings from `govulncheck` (none were reachable from this code, symbol-level scan was already clean):
  - GO-2026-6866 / GO-2026-6860: `golang.org/x/net` html parser namespace-transition and quadratic text-node coalescing issues, fixed in v0.62.0.
  - GO-2026-5932: unmaintained `golang.org/x/crypto/openpgp` module-level advisory, superseded by v0.59.0.
- Transitive bumps: `x/sync` v0.25.0, `x/sys` v0.50.0, `x/text` v0.44.0.

### Fixed

- `TestRecordConnectionAccountBanFailure_EscalatesAndClears` no longer fails on Windows: the clock has ~15 ms resolution, so two consecutive ban records inside one escalation step could produce identical park-until stamps. Strict outlasting is now only asserted when the escalation window actually grew.

### Verification

- `govulncheck ./...` clean before and after; the three findings were dependency-level only (0 symbol-level).
- Portability gate: all five `CROSS_TARGETS` build with `CGO_ENABLED=0`.
- `go test ./...` passes except two pre-existing environment issues on this workstation (production `.env` overriding `TestLoadConfigFromDotEnv` fixtures); the fixed flake passes `-count=3`.
