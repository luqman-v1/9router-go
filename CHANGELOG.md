# Changelog

## [v1.1.0] — 2026-07-21

### 🚀 New Features

- **SSRF protection** — `/v1/web/fetch` now blocks requests to private/internal IPs (RFC 1918, loopback, link-local, cloud metadata). Matches Next.js `assertPublicUrl()`. (`internal/handlerutil/ssrf.go`)
- **Bypass handler** — Detects Claude Code naming, warmup, and count requests. Returns fake responses without calling upstream, preventing wasted combo rotation slots. (`internal/handlers/bypass.go`)
- **Structured logging** — New `internal/log` package with Info/Warn/Error/Debug levels, runtime config via `LOG_LEVEL` env var. All ~100 `log.Printf` calls replaced across 24 files.
- **Per-connection model locks** — Model locks now stored as `modelLock_<model>` in `providerConnections.data` JSON blob. DB-compatible with Next.js dashboard. Connection A and B can have independent lock states.
- **SSE stall detection** — `StallReader` wrapper closes upstream connection after 6 minutes of no data, preventing hung streams. Integrated into all 4 SSE stream paths.
- **Error classification** — Text-based error rules (8 patterns) + status-based rules (5 codes) + exponential backoff (2s–5min). Fully matching Next.js `checkFallbackError()`.
- **Retry-after tracking** — Tracks earliest `retryAfter` across combo models, includes `Retry-After` header in error responses.
- **Request ID tracing** — Every response includes `X-Request-ID` header, access log includes `id=xxx` prefix.
- **Combo strategies aligned with Next.js** — Sticky round-robin, auto-capability-switch (vision/pdf detection).
- **Health/lock check in combo loops** — Skip unhealthy or locked models during fallback iteration.

### 🔧 Refactoring

- **Error response consistency** — `WriteJSONError` now status-code-aware (e.g., 401 → `authentication_error`, 429 → `rate_limit_error`). `auth.go` inline JSON replaced.
- **SSE consolidation** — `proxy.WriteSSEHeaders` shared by all 4 SSE stream functions. `proxy.SSECopy` with optional `onChunk` callback.
- **Shared test fixture** — `internal/dbtest` package provides canonical `CreateTables()` eliminating duplicated schema in 5+ test files.
- **`stringBuilder` → `bytes.Buffer`** — Removed duplicate custom type in favor of standard library.

### 📚 Documentation

- `ARCHITECTURE.md` — 10 Mermaid flow diagrams (request lifecycle, combo, fusion, error classification, etc.)
- `DATABASE.md` — All 11 tables, JSON blob structure, Go vs Next.js differences

### 🐛 Fixes

- `RetryAfter` ceiling calculation corrected from floor to proper ceiling (`time.Second - 1`)
- Stream translation now handles `[DONE]` marker before JSON parsing
- `TranslateResp` field now passed in `tryForwardWithConnection`

## [v1.0.2] — Previous

- Initial release with OpenAI/Claude SSE proxy, combo fallback, token savers, benchmark results.
