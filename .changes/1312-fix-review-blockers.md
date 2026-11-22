### Fix review blockers for PostgreSQL engine (#1312 follow-up)

Follow-up fixes for the four issues raised in review of #1312:

- **`DATABASE_URL` now selects the engine**: `LoadConfigFromViper` maps `DATABASE_URL` to `DatabasePath`, taking precedence over `DB_PATH`. The documented `deploy/docker-compose.yml` setup now actually starts on PostgreSQL instead of silently using SQLite.
- **Import no longer loses seeded data**: `dbmigrate.Import` bootstraps the PG schema without the default `settings`/`_meta` rows, replays the dump, then seeds only genuinely empty rows. A regression test asserts the exported `settings.data` value survives (the CI smoke previously only checked `COUNT(1)`).
- **`json_set` works with production argument shapes on PG**: the rebind driver rewrites SQLite `json_set(...)` to a new `pg_json_set` shim that accepts TEXT args; integers, `json()`, `NULL` and JSON objects now bind correctly. Added an integration test covering `LockConnectionModel`, `RecordConnectionError`, `UnlockConnectionModel`, `ClearConnectionRateLimit` and OAuth lock shapes.
- **`CurrentEngine()` can no longer go stale**: it derives the engine from the global handle's driver instead of a separately-maintained variable.

Verified: `go test ./...`, PG16 integration tests against `postgres:16-alpine` (`TestPGJSONSetProductionShapes`, `TestImportPreservesSeededValues`, `TestJSONShimDifferential`), `go mod tidy`, `CGO_ENABLED=0` build.
