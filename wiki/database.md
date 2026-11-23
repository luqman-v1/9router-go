---
title: Database
order: 11
section: Under the Hood
since: 1.9.14
---

# Database

No conversion is needed to read an upstream 9Router SQLite database — point
9router-go at it and it renders the same data. (Moving 9router-go's own data
to PostgreSQL is different, and is covered below.)

## Engines

Two engines, selected by `DATABASE_URL`:

| Engine | When | Selected by |
|---|---|---|
| SQLite (default) | Zero-config, one file, the upstream-compatible shape | `DATABASE_URL` unset or a file path |
| PostgreSQL (opt-in) | Many writers, a database you already back up and monitor, or a box where the ledger outgrew a single file | `DATABASE_URL` starting with `postgres://` or `postgresql://` |

`DATABASE_URL` takes precedence over `DB_PATH`: a `postgres://` DSN means the
gateway talks to PostgreSQL, anything else is a SQLite file path. The engine
is the only thing the DSN decides — provider/model routing never inspects it.

## Location

| Engine | Path |
|---|---|
| SQLite, macOS / Linux | `~/.9router/db/data.sqlite` |
| SQLite, Windows | `%APPDATA%\9router\db\data.sqlite` |
| SQLite, Docker | `/data/db/data.sqlite` inside the container |
| PostgreSQL | wherever `DATABASE_URL` points |

## Sharing with 9Router

Stop 9Router, run 9router-go, and log in with your existing password. Your
provider connections, proxy pools, combos, API keys, model aliases, and usage
history are all there — 9router-go's dashboard renders them because it *is* the
same data.

You **do not have to stop 9Router** if you want to keep the Node.js dashboard
around. Run 9router-go on a different port against the same `DATA_DIR`:

```bash
# 9Router on :20128 (Next.js)  ·  9router-go on :20130 (Go, same DB)
PORT=20130 DATA_DIR=/home/you/.9router ./9router-go
```

Both read the same tables, so a connection you add in either dashboard shows up
in the other. SQLite serialises the writes; the supported setup is one active
writer, so avoid editing the same combo or settings entry in both at the same
moment.

**Running it the other way round** (9router-go first, then pointing 9Router at the
same directory) works the same way. 9router-go will have added two columns to
`providerConnections` (`lastUsedAt`, `consecutiveUseCount`) and one extra table
(`upstream_leases`). Upstream ignores both — its schema sync is additive and
doesn't drop unknown columns.

## Moving from SQLite to PostgreSQL

The gateway ships a migration path, and it is two commands:

```bash
# 1. Export every table of the SQLite database to JSONL
9router-go db export --from ~/.9router/db/data.sqlite --out ./db-dump

# 2. Import the dump into PostgreSQL
9router-go db import --to "postgres://user:pass@host:5432/9router" --from ./db-dump
```

Then run the gateway against PostgreSQL:

```bash
DATABASE_URL="postgres://user:pass@host:5432/9router" ./9router-go
```

The import normalises the SQLite shapes PostgreSQL rejects out of the box:
`0/1` integers become `bool` for `BOOLEAN` columns, NUL bytes are stripped
from `TEXT` (the credential mask carries them internally), and sequence
resets skip tables whose primary key is not an integer. Schema parity is
additive on both engines, so a migration lands with the same columns the
SQLite bootstrap would have created.

Connection pooling is PostgreSQL-only and tuned from the environment:
`DB_MAX_CONNS` / `DB_MAX_IDLE_CONNS`, clamped to sane ceilings. SQLite
keeps WAL mode and the per-connection PRAGMAs the Node/Bun upstream uses.

## What it does not do

- **No legacy JSON import.** If your 9Router data still lives in old JSON files,
  start upstream once to convert them, then switch.
- **No destructive migrations or pre-migration backups.** The schema bootstrap
  only ever *adds* — create missing tables, add missing columns, seed missing
  rows. The SQLite-to-PostgreSQL import above is a separate command you run
  explicitly; it writes the target database and never touches the SQLite
  source.
- **No interpretation of `_meta.schemaVersion`.** That is upstream's migration
  bookkeeping. A database written by a much newer 9Router release should be
  checked before you rely on it.

## Backup

Take a backup first if the database matters:

```bash
cp -r ~/.9router/db ~/9router-db-backup   # stop the daemon first
```

## Binary portability

One binary per target, built from pure Go source with `CGO_ENABLED=0`. Every
release target must still build — that is the only thing constraining a
dependency here, not its size.
