# Migrating SQLite → PostgreSQL

One command to move an existing SQLite install, one `docker compose up` to run
on PostgreSQL.

## CLI (already built)

```bash
# 1. Dump SQLite to JSONL (read-only, safe to run on a live gateway)
9router-go db export --from ~/.9router/db/data.sqlite --out ./migrate/

# 2. Load into PostgreSQL (idempotent: re-runs are safe)
9router-go db import --to "postgres://router:pw@localhost:5432/router?sslmode=disable" --from ./migrate/
```

## Cutover (PRD §4.4 Fase 3)

1. Stop traffic to the gateway (`systemctl stop 9router-go` or `docker stop`).
2. Run `9router-go db export` against the last SQLite file.
3. Run `9router-go db import` against the new PostgreSQL instance.
4. Set `DATABASE_URL`; remove/comment `DB_PATH`.
5. Start the gateway.
6. Verify `GET /api/connections` and `GET /api/usage`; the boot log should read
   `database engine: postgres`.

**Rollback**: unset `DATABASE_URL`, restart. The SQLite file is never touched.

## Docker (no local CLI needed)

```bash
cd deploy/
cp .env.example .env
$EDITOR .env                         # set POSTGRES_PASSWORD

docker compose up -d db              # start PostgreSQL
docker compose --profile migrate up migrate   # export from SQLite, import into PG
docker compose --profile migrate down
docker compose up -d                 # start the gateway on PostgreSQL
```

The `migrate` service reads `~/.9router/db/data.sqlite` (read-only) and writes
JSONL into `./migrate/`. Re-running the migrate profile is safe: every row is
inserted with `ON CONFLICT DO NOTHING`.
