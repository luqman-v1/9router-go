### Added

- Qoder and Qoder CN daily credit check-in, ported from the merged upstream
  `magpie-community/plugins` `packages/qoder` contract (v0.2.71, commit
  `afe4c0864`, 2026-11-17). A bounded worker polls each account's `CLAIM_BENEFIT`
  campaigns every 30 minutes and at the Beijing day boundary, claims one
  campaign with the documented `POST …/{id}/claim`, and records the outcome in
  `<DATA_DIR>/qoder-checkin.json`. The `qoderCheckin` setting gates it at runtime
  and `GET /api/qoder/checkins` exposes the last result per account. Both sites
  keep separate hosts and credentials (§3: no cross-provider aliasing).
- `AGENTS.md` now records `yetone/magpie` and its community Qoder plugin as
  merged-only secondary references for Qoder wire-contract work, with pinned
  commits and a porting protocol.

### Verification

- `go test -tags=integration ./internal/integration/ -run TestQoderCheckinsEndpoint` — verifies the read-only state endpoint on the production router.
- `go test ./internal/qodercheckin/...` — 8 tests covering claim, already-claimed,
  inactive, refusal, expired sign-in, site isolation, ledger persistence and
  outcome storage.
- `go build ./internal/app/... ./internal/handlers/dashboard/... ./internal/qodercheckin/...`.
