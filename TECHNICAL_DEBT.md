# Technical Debt & Concurrency Audit

Documented technical debt, concurrency risks, resource leaks, and planned architectural improvements for `9router-go`.

---

## 🔴 Open Critical Items (Priority 1: Immediate Fix Required)

*No open critical items.*

---

## 🟠 Open High-Priority Items (Priority 2: Memory & Resource Leak Prevention)

*No open high-priority items.*

---

## 🟡 Open Medium Items (Priority 3: Code Robustness & Performance)

*No open medium items.*

---

## ✅ Resolved Items

- **`getString` Helper Consolidation**: Consolidated into `internal/handlerutil.GetString`. Removed duplicates from `token.go`, `proxyPools.go`, and `providers/oauth.go`.
- **Combo Handler Domain Modularization**: Refactored `handlers/` into domain-driven subpackages (`chat`, `media`, `oauth`, `shared`).
- **Paket Dead-Code Cleanup**: Removed unused legacy `internal/proxy/handlers` package.
- **Observability & Request Tracing**: Added Correlation ID (`X-Request-ID`), structured logging (`InfoCtx`), secret masking (`MaskSecret`), and latency/TTFT tracking.
- **App Version & Auto-Update Engine**: Implemented `internal/updater` package for semver version checking, REST endpoints (`/api/version`), and self-updating.
- **`defer resp.Body.Close()` Inside Loop in Media Fallback** (was Item 6): Fixed in `internal/handlers/media/media.go`. Failed combo attempts now explicitly call `resp.Body.Close()` before `continue`, preventing connection pool exhaustion from dangling response bodies.
- **Global Variable Cross-Request Contamination on `translator.lastUsage`** (Item 1): Implemented `WithUsageCapture`, `SetUsage`, and `GetAndClearUsage` via `context.Context` in `internal/translator/usage.go` and threaded context across handlers & executors.
- **Read-Modify-Write Race Condition in `upsertDailyUsage`** (Item 2): Protected `upsertDailyUsage()` with `dailyUsageMu` mutex in `internal/handlers/chat/usage.go` to serialize daily JSON updates.
- **Fallback Retry After Headers Written on SSE Streams** (Item 3): Implemented `committedResponseWriter` in `internal/handlers/chat/response_writer.go` and checked header state before attempting error response/retries.
- **Missing `context.Context` Propagation on Upstream HTTP Requests** (Item 4): Replaced `http.NewRequest` with `http.NewRequestWithContext(ctx, ...)` across all proxy drivers, OAuth helpers, and HTTP handlers.
- **Lack of Graceful Server Shutdown** (Item 5): Refactored `cmd/9router-go/main.go` to use `http.Server` with signal notification (`SIGINT`/`SIGTERM`) and graceful `server.Shutdown(ctx)` with a drain timeout.
- **SQLite Connection Pool Limit Too High** (Item 7): Reduced `SetMaxOpenConns` to 4 in `internal/db/client.go` to minimize write lock contention in SQLite WAL mode.
- **ProxyPool Round-Robin Counter Resets Per Request** (Item 8): Cached `ProxyPool` instances using a thread-safe `sync.Map` in `internal/db/proxyPools.go` to preserve atomic round-robin counters across HTTP requests.
- **Unbounded Request Body Reading** (Item 9): Added `middleware.MaxBody(10MB)` in `internal/middleware/max_body.go` and registered it globally in `cmd/9router-go/main.go`.
