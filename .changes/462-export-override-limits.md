### Export the provider-override limits

- `internal/db/provider_overrides.go`: the two header limits are now readable as
  `db.MaxOverrideHeaders` and `db.MaxOverrideHeaderValueSize` instead of staying
  package-private. They are unchanged in value (20 headers, 8192 bytes) — this is
  an export, not a new ceiling.
- Why: the dashboard's settings import reports an over-limit header as a warning
  and keeps going, while the write path rejects it. A reporter that has to
  restate "20" would drift from the enforcer the first time either changes, so
  the reporter reads the same constants the enforcer does.
- Evidence: `go build ./internal/db/`, `go vet ./internal/db/`, and
  `go test ./internal/db/ -count=1` all pass offline
  (`GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off`). No behaviour changes, so there is
  no red test: the change adds a name for an existing value.
