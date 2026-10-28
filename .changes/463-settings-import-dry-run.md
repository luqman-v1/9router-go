### Settings import gains a dry run and a lossless export

- `internal/handlers/dashboard/settings.go`: `POST /api/settings/import` accepts
  `?dryRun` (and the same flag in the body) and answers with the report it would
  have written, without writing. The report names the key count, the allowlist
  shape, and any header that exceeds the override limits.
- The export walk now runs inside one read transaction. Reading table by table on
  a bare `*sql.DB` let a write land between two queries, so a key deleted between
  the `apiKeys` query and the access query exported as a key with no allowlist —
  which restores as unrestricted.
- Each key row now emits both `allowedModels` (what a Go instance reads back) and
  `access` (what the Node dashboard reads). Emitting only one of them meant a
  backup taken here restored unrestricted there.
- Evidence: `go build ./...` and `go test ./... -count=1` pass offline
  (`GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off`). `settings_import_g05_test.go`
  covers the dry run, the unrestricted/restricted-empty shapes and the header
  warnings.
- Note for reviewers: this depends on `db.MaxOverrideHeaders` /
  `db.MaxOverrideHeaderValueSize` and on `db.SentinelDenyAll`. The first two come
  from the preceding commit in this branch; the third is from #451.
