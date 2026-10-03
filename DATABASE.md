# SQLite Database and Operator Contract

This document describes the storage contract implemented by `9router-go` v1.9.1. It deliberately separates:

- the core schema that the Go runtime **expects to already exist**;
- the one Go-only table the runtime creates at startup;
- compatibility with the historical upstream Next.js database; and
- the limited, dashboard-triggered backup feature.

The current Go source is the behavioral authority. The local upstream checkout (`decolua/9router` v0.5.85) is a compatibility reference, not a claim that the two products are identical.

## Storage location

`DB_PATH` takes precedence. Otherwise the database is:

| Platform | Default path |
|----------|--------------|
| macOS/Linux | `$DATA_DIR/db/data.sqlite`, or `~/.9router/db/data.sqlite` |
| Windows | `%APPDATA%\9router\db\data.sqlite` |
| Docker | configured through the mounted data directory |

`DATA_DIR` overrides the platform data root. When `DB_PATH` names an existing directory, the resolver recognizes compatible layouts in this order: `db/data.sqlite`, `data.sqlite`, then `9router.db` (`internal/config/config.go`).

SQLite is opened through `modernc.org/sqlite` with WAL, `synchronous=NORMAL`, foreign keys, a 5-second busy timeout, and a maximum of four open connections (`internal/db/client.go`).

## Schema bootstrap and versioning

### Current Go behavior

Production startup bootstraps the upstream core schema additively via `db.EnsureCoreSchema` (`internal/db/schema.go`, wired in `internal/app/database.go`):

1. opens and configures the SQLite file;
2. creates the 11 upstream core tables/indexes from `schema.js` `TABLES` when absent (`settings`, `_meta`, `providerConnections`, `providerNodes`, `proxyPools`, `apiKeys`, `combos`, `kv`, `usageHistory`, `usageDaily`, `requestDetails`);
3. backfills columns declared upstream but missing on legacy databases (same strip-`PRIMARY KEY`/`UNIQUE` guard as upstream `syncSchemaFromTables`), plus Go-only `providerConnections.lastUsedAt` / `consecutiveUseCount` (upstream ignores them);
4. seeds `_meta.schemaVersion = '1'` and an empty `settings` row (`id=1, data='{}'`), both `INSERT OR IGNORE`; and
5. creates Go-only `upstream_leases` with `CREATE TABLE IF NOT EXISTS`.

Every statement is idempotent, so existing user data is never touched — a fresh `DATA_DIR` now boots into a working installation (login with the compatibility password, then add providers/keys from the dashboard). Go still does not import legacy JSON files, run destructive migrations, or take pre-migration backups; for those, start the upstream application once as before.

`internal/dbtest.SchemaStatements()` creates tables for Go tests only. It is not the production path and must not be presented as the deployed schema.

### Upstream behavior

The historical Next.js application owns a versioned SQLite layer:

- `src/lib/db/schema.js` declares `SCHEMA_VERSION = 1` and the core table/index schema;
- `src/lib/db/migrate.js` runs versioned and additive migrations;
- `src/lib/db/backup.js` writes pre-migration safety backups; and
- `src/lib/db/paths.js` resolves the same default `DATA_DIR/db/data.sqlite` location.

Starting that upstream application against a compatible DB is the supported way to bootstrap or migrate the core schema today. This is compatibility with upstream persistence, not a claim that Go has implemented the upstream migration system.

## Core tables consumed by Go

The following is the v0.5.85 upstream core schema consumed by the Go repositories. Go does not create these definitions in production.

### `settings`

Single-row global configuration JSON:

```sql
CREATE TABLE settings (
    id   INTEGER PRIMARY KEY CHECK (id = 1),
    data TEXT NOT NULL
);
```

Examples include login and SSO configuration, token savers, combo/provider strategies, proxy-pool routing, auto-update, and the Headroom URL. Provider credentials remain in `providerConnections`; the dashboard password is stored as a bcrypt hash in this JSON.

### `providerConnections`

```sql
CREATE TABLE providerConnections (
    id          TEXT PRIMARY KEY,
    provider    TEXT NOT NULL,
    authType    TEXT NOT NULL,
    name        TEXT,
    email       TEXT,
    priority    INTEGER,
    isActive    INTEGER DEFAULT 1,
    data        TEXT NOT NULL,
    createdAt   TEXT NOT NULL,
    updatedAt   TEXT NOT NULL
);
```

`data` is plaintext JSON. Depending on provider, it can contain `apiKey`, `accessToken`, `refreshToken`, expiry/scope data, provider-specific client secrets, proxy configuration, quota state, and `modelLock_<model>` entries.

Several successful request paths also call `UpdateConnectionLastUsed`, which requires the Go-only columns below:

```sql
lastUsedAt          TEXT
consecutiveUseCount INTEGER DEFAULT 0
```

They are not in the upstream v0.5.85 schema, but Go **does** add them:
`EnsureAdditiveColumns` backfills both on every startup, so an upstream database
gains them rather than failing the update.

`lastUsedAt` carries the round-robin stamp and is compared as a **string**, so it
is written in a fixed-width format (`internal/db/rotation.go`). Two rules keep
that comparison meaningful, and both are load-bearing:

- The stamp must sort strictly after the largest stamp already stored. The wall
  clock does not guarantee this — its resolution is the platform's, and on
  Windows `time.Now` can repeat across several calls — so `stampConnection`
  advances the value past the stored maximum inside a `BEGIN IMMEDIATE`
  transaction.
- Values written by older builds are plain second-precision RFC3339. `…:00Z`
  sorts *after* `…:00.000000001Z` because `'.'` (0x2E) beats `'Z'` (0x5A), so a
  nanosecond bump cannot overtake such a row and must advance a full second.

### `providerNodes`

```sql
CREATE TABLE providerNodes (
    id        TEXT PRIMARY KEY,
    type      TEXT,
    name      TEXT,
    data      TEXT NOT NULL,
    createdAt TEXT NOT NULL,
    updatedAt TEXT NOT NULL
);
```

Stores custom/compatible provider nodes and their JSON configuration.

### `proxyPools`

```sql
CREATE TABLE proxyPools (
    id         TEXT PRIMARY KEY,
    isActive   INTEGER DEFAULT 1,
    testStatus TEXT,
    data       TEXT NOT NULL,
    createdAt  TEXT NOT NULL,
    updatedAt  TEXT NOT NULL
);
```

`data` can include proxy URLs or credentials, relay type, no-proxy rules, and rotation strategy.

### `apiKeys`

```sql
CREATE TABLE apiKeys (
    id        TEXT PRIMARY KEY,
    key       TEXT UNIQUE NOT NULL,
    name      TEXT,
    machineId TEXT,
    isActive  INTEGER DEFAULT 1,
    createdAt TEXT NOT NULL
);
```

Client keys are stored in plaintext and validated on proxy requests.

### `combos`

```sql
CREATE TABLE combos (
    id        TEXT PRIMARY KEY,
    name      TEXT UNIQUE NOT NULL,
    kind      TEXT,
    models    TEXT NOT NULL,
    createdAt TEXT NOT NULL,
    updatedAt TEXT NOT NULL
);
```

The current upstream contract does not have a `combos.strategy` column. Go reads strategy from `settings.comboStrategies` (with global fallbacks); an optional `strategy` column is attempted opportunistically during combo create/update, but is not part of the declared schema.

### `kv`

```sql
CREATE TABLE kv (
    scope TEXT NOT NULL,
    key   TEXT NOT NULL,
    value TEXT NOT NULL,
    PRIMARY KEY (scope, key)
);
```

Important scopes include `modelAliases`, `customModels`, `pricing`, `mitmAlias`, and `disabledModels`. Legacy `modelLock` and `providerHealth` data may also be present. `customModels` is a list encoded in the `value` JSON with a compound key; other scopes commonly use `value` as a JSON string.

### `usageHistory`

```sql
CREATE TABLE usageHistory (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp        TEXT NOT NULL,
    provider         TEXT,
    model            TEXT,
    connectionId     TEXT,
    apiKey           TEXT,
    endpoint         TEXT,
    promptTokens     INTEGER DEFAULT 0,
    completionTokens INTEGER DEFAULT 0,
    cost             REAL DEFAULT 0,
    status           TEXT,
    tokens           TEXT,
    meta             TEXT
);
```

Go omits `id` on insert, allowing the upstream autoincrement column to allocate it, and reads recent rows by implicit `rowid`. Go test fixtures sometimes omit `id`; that fixture difference is not a production schema.

### `usageDaily`

```sql
CREATE TABLE usageDaily (
    dateKey TEXT PRIMARY KEY,
    data    TEXT NOT NULL
);
```

One pre-merged JSON aggregate per date. Go protects its read/merge/write path with an in-process mutex, then performs `INSERT OR REPLACE`.

### `requestDetails`

```sql
CREATE TABLE requestDetails (
    id           TEXT PRIMARY KEY,
    timestamp    TEXT NOT NULL,
    provider     TEXT,
    model        TEXT,
    connectionId TEXT,
    status       TEXT,
    data         TEXT NOT NULL
);
```

The JSON `data` can contain request messages, response content, token counts, and latency/TTFT. Go currently records truncated content, not an opt-in debug-only sample.

## Go-only table

### `upstream_leases`

`EnsureUpstreamLeases` creates this table idempotently at every startup:

```sql
CREATE TABLE IF NOT EXISTS upstream_leases (
    scope      TEXT NOT NULL,
    key        TEXT NOT NULL,
    value      TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (scope, key)
);
```

The registered `freebuff-session` scope coordinates admission for one hashed token/model key across cooperating processes. Keys are SHA-256 hashes and values are opaque instance IDs; raw credentials do not belong in this table. The upstream Next.js app does not know this table and will ignore it.

This table is a narrow coordination primitive, not a general distributed-state layer.

## Upstream-only table

### `_meta`

```sql
CREATE TABLE _meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
```

Upstream uses it for backup/migration/app metadata. Go neither creates nor reads it. Its presence does not prove that Go is compatible with every schema version recorded there; operators must test the specific schema and required optional columns.

## Compatibility boundaries

| Area | Current behavior | Operator interpretation |
|------|------------------|-------------------------|
| Default path | Go and upstream v0.5.85 both use `DATA_DIR/db/data.sqlite` | Compatible in the common configuration |
| Core columns | Most Go reads/inserts follow the upstream v0.5.85 schema | Compatible only for paths exercised against that exact schema |
| Go connection metadata | Go backfills `lastUsedAt` / `consecutiveUseCount` additively on startup | Safe on both runtimes; upstream ignores the extra columns |
| Schema creation/migration | Go creates the 11 core tables + backfills columns + seeds `_meta`/`settings`; no legacy-JSON import, no destructive migrations, no pre-migration backups | Fresh DB is a supported bootstrap; for legacy-JSON import or schema repair, start upstream once |
| Extra table | Upstream ignores `upstream_leases` | Generally harmless; back up separately if lease continuity matters |
| JSON payloads | `providerConnections.data` and `kv` are shared conventions | Shape compatibility is field-by-field, not guaranteed by a version check |
| Write coordination | SQLite serializes writes; only lease admission is explicitly cross-process | Use one active 9router-go writer unless the workload is tested |

## Backup scope

There are two different things called “backup” in the product. Neither should be mistaken for a complete physical SQLite snapshot unless verified.

### Dashboard JSON export/import

`GET /api/settings/database` exports a shared dashboard payload containing:

- `settings`;
- `providerConnections` and `providerNodes` with their `data` JSON merged into rows;
- `proxyPools`;
- client `apiKeys`;
- `combos`; and
- the `modelAliases`, `customModels`, `mitmAlias`, and `pricing` KV scopes.

The payload is sensitive: it can contain provider API keys/tokens, proxy credentials, client API keys, and the dashboard password hash. It does **not** include `usageHistory`, `usageDaily`, `requestDetails`, `upstream_leases`, `_meta`, or every KV scope. Import is destructive: it deletes and replaces the listed configuration data in one transaction. Treat it as configuration export/restore, not a full disaster-recovery backup.

The route is always protected from client API keys and unauthenticated access; it requires a valid dashboard session/local CLI token plus current-password re-authentication where applicable (`internal/middleware/dashboard_auth.go`, `internal/handlers/dashboard/settings.go`).

### Physical SQLite backup

For a full restore point, copy the SQLite database consistently. The simplest operator procedure is to stop 9router-go and copy the whole database directory, including `data.sqlite-wal` and `data.sqlite-shm` if present. For a live backup, use a SQLite-aware online backup/checkpoint procedure; do not copy only the main file while WAL contains committed pages.

Upstream's automatic pre-schema backup is separate: it lives under `db/backups/`, keeps only the newest three, and intentionally excludes `requestDetails`. Go does not create those upstream migration backups.

## Permissions and secret handling

- The parent data directory is initially created with mode `0755`.
- On DB open, the database's immediate parent is changed to `0700` (the chmod error is currently ignored).
- The main DB file is changed to `0600` on every open.
- WAL/SHM files inherit protection from the private parent directory, but deployments using unusual ACLs, network filesystems, containers, or backups must verify them separately.
- Provider credentials and client API keys are plaintext in SQLite. Dashboard API responses sanitize many secrets, and dashboard list endpoints do not reveal full client keys, but at-rest encryption is not implemented.

Backups and diagnostic exports inherit the same confidentiality requirements as the live DB.

## Multi-process limitations

SQLite WAL and the five-second busy timeout reduce lock failures; they do not make all application state multi-process safe.

- `usageDaily` is a read/merge/full-row replace guarded only by a process-local mutex. Concurrent processes can lose daily aggregate updates.
- `UpdateSettingsRaw` performs read/merge/write without a compare-and-swap or cross-process lock. Concurrent settings changes can overwrite each other.
- Proxy-pool round-robin state and several provider/session/quota caches are process-local. Two active instances can choose different rotations or independently refresh/hold state.
- `upstream_leases` provides cross-process coordination only for registered lease scopes.

The supported operational model is one active 9router-go process writing a database. Co-running upstream Next.js and Go against the same DB may be useful for migration/testing, but it is not a conflict-free HA topology.

## Operator checklist

Before production use or an upgrade:

- [ ] Confirm the exact DB path with `DATA_DIR`/`DB_PATH`; do not assume `9router.db`.
- [ ] Run the upstream v0.5.85 application once (or apply a reviewed upstream migration) to create/migrate the core schema; a Go-only blank DB is insufficient.
- [ ] Verify required tables and columns, especially the two optional connection-metadata columns used by Go success paths.
- [ ] Check `_meta` separately if the DB came from upstream; Go does not interpret it.
- [ ] Verify `journal_mode=wal`, a healthy write/read test, and sufficient free space for WAL growth.
- [ ] Confirm the DB parent is private and the main file/WAL/SHM are not exposed to other OS users or public volumes.
- [ ] Decide whether prompt/response content retention is acceptable for `requestDetails`.
- [ ] Run only one active Go writer unless a specific multi-process workflow has been tested.
- [ ] Take a consistent full SQLite backup before schema changes, upgrades, imports, or destructive operations.
- [ ] Store dashboard JSON exports and physical backups as secrets; test restore in a separate directory.
- [ ] Do not treat the dashboard JSON export as a full usage/telemetry/lease backup.
- [ ] If sharing with upstream Next.js, stop writers during migration/import and verify its additive sync does not remove required data.

## Source references

- Go DB open and PRAGMAs: `internal/db/client.go`
- Production startup and lease creation: `internal/app/database.go`
- Lease implementation: `internal/db/leases.go`
- Usage and diagnostics persistence: `internal/db/usage.go`
- Dashboard configuration backup: `internal/handlers/dashboard/settings.go`
- Upstream schema/migrations/backup: `src/lib/db/schema.js`, `src/lib/db/migrate.js`, `src/lib/db/backup.js`
