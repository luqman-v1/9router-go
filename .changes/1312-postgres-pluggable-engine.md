### PostgreSQL as a pluggable database engine

The gateway now runs on either SQLite (default) or PostgreSQL, selected by the `DATABASE_URL` DSN prefix. Self-host single-user installs keep zero-config SQLite; deployments that need more write throughput set `DATABASE_URL` to a `postgres://` DSN and the same binary switches engines at startup.

- **Engine detection** via `DATABASE_URL` prefix (`postgres://` or `postgresql://`). PG connection pool defaults to 50 open / 10 idle, overridable with `DB_MAX_CONNS` and `DB_MAX_IDLE_CONNS`.
- **Dual schema** (`internal/db/schema_postgres.go`): a PG mirror of the core schema using `BIGINT GENERATED ALWAYS AS IDENTITY`, `BOOLEAN`, and `BYTEA`, all `IF NOT EXISTS`, so bootstrap stays idempotent. Timestamps remain TEXT in v1 so the SQLite -> PG export/import is a straight copy.
- **Portable queries across the repo**: `INSERT OR IGNORE/REPLACE` replaced with `ON CONFLICT`, `datetime('now')` replaced with Go-computed timestamps passed as bind args, and SQLite-only `PRAGMA` calls guarded behind an engine check. A repo-wide lint test (`portable_sql_test.go`) fails the build if a SQLite-only construct reappears outside tests and schema files.
- **Migration tooling**: `9router-go db export --from <sqlite> --out <dir>` writes one JSONL per user table; `9router-go db import --to <pg-dsn> --from <dir>` replays them in chunked `ON CONFLICT DO NOTHING` transactions and resets `IDENTITY` sequences. Export opens SQLite read-only, and both commands are safe to re-run.
- **Docker Compose** (`deploy/`): `postgres:16-alpine` plus the gateway service with write-heavy PG tuning, and a one-shot `migrate` profile that mounts the existing SQLite file read-only and runs export + import inside the compose network. Cutover and rollback steps are in `deploy/README.md`.

**Why**: SQLite's WAL mode allows only one writer at a time. Every `INSERT usageHistory`, `UPDATE api_keys`, and `INSERT requestDetails` queues on `busy_timeout`, and the four-connection pool turns that queue into the dominant tail latency once write throughput climbs. PostgreSQL's MVCC removes the single-writer lock, and its connection pool hands the load to many connections instead of one.

**How verified**: `go test ./...` green on SQLite. `detectEngine` covered by a table-driven unit test. The Postgres path is exercised through the `deploy/` compose stack on a live PostgreSQL 16 instance.

Upstream decolua/9router keeps SQLite as its only engine; this is a `9router-go` extension and diverges deliberately.
