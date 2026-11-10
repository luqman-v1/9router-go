### Features

- **Tamper-evident admin audit trail** (port of KeiRouter `backend/internal/audit`, MIT). Every mutating
  dashboard request, plus the sensitive reads that hand out secrets (`/api/settings/database`,
  `/api/usage/export`, `/api/providers/{id}/export-keys`, `/api/audit/export`) and login/logout
  attempts, is recorded into a new Go-only `audit_log` table with a per-writer hash chain
  (`chain_id` / `chain_seq` / `prev_hash` / `hash`, HMAC-SHA256). The chain key comes from
  `ROUTER_MASTER_KEY` when set, else a random 32-byte key persisted in settings on first boot, so
  rows stay verifiable across restarts. Auditing is on by default; `Record` never blocks (bounded
  queue, drop-and-count), so it cannot slow down or fail an admin request.
- **Admin endpoints**: `GET /api/audit` (cursor-paginated list with action/actor/outcome/resource/
  q/since/until filters), `GET /api/audit/export` (JSON Lines, SIEM-ready, capped at 200k rows per
  request), `GET /api/audit/verify` (recomputes every hash chain; a break means a row was modified,
  deleted, or re-linked), `GET /api/audit/status` (recorder counters, retention, OTLP exporter
  counters).
- **Optional OTLP/HTTP export** (pure stdlib, no new dependency): when
  `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` or `OTEL_EXPORTER_OTLP_ENDPOINT` is set, committed rows are
  also shipped to the collector in the standard OTel logs JSON shape (gzip, bounded queue,
  Retry-After-aware retries, `keirouter.audit.*` attribute namespaces kept for SIEM parity with
  upstream KeiRouter).
- **Retention**: `audit_retention_days` in settings (default 0 = keep forever); an hourly sweeper
  deletes rows past the window when a window is configured.

Why: an operator who can read the SQLite file could previously rewrite the admin history. The hash
chain makes any such edit visible through `/api/audit/verify` without leaving the gateway.

Verified: `go test ./...` (incl. `internal/audit` chain/tampering, `internal/handlers/dashboard`
middleware + endpoint, OTLP exporter wire-shape tests) and `make test-integration` (real fx boot:
recorder starts in `ServerModule.OnStart`, drains in `OnStop`). No cgo, no new `go.mod` entries
(`go mod tidy` clean); `web` bundle unchanged.
