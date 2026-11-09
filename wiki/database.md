---
title: Database
order: 11
section: Under the Hood
since: 1.9.14
---

# Database

9router-go reads the upstream 9Router database as-is. There is no import, export,
or migration.

## Location

| | Path |
|---|---|
| macOS / Linux | `~/.9router/db/data.sqlite` |
| Windows | `%APPDATA%\9router\db\data.sqlite` |
| Docker | `/data/db/data.sqlite` inside the container |

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

## What it does not do

- **No legacy JSON import.** If your 9Router data still lives in old JSON files,
  start upstream once to convert them, then switch.
- **No destructive migrations or pre-migration backups.** Its bootstrap only ever
  *adds* — create missing tables, add missing columns, seed missing rows.
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
