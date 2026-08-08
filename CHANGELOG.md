# Changelog

## [v1.7.1] — 2026-08-08

### 🐛 Bug Fixes

- **Cached-token parity across every provider** — Prompt-cache accounting no longer works only for antigravity. Gemini `usageMetadata.cachedContentToken` now flows through both non-stream and stream translation into OpenAI `usage.cached_tokens`; the `!translate` response path uses a dual-format parser (`ParseResponseUsage`) that reads Claude `cache_read_input_tokens`/`cache_creation_input_tokens` and OpenAI `prompt_tokens_details.cached_tokens`, so cached tokens survive any provider → OpenAI → Claude double translation. (`internal/translator/gemini.go`, `internal/translator/response.go`)
- **Gemini tool-schema `const` re-injection** — `stripUnsupported` now re-runs after `anyOf`/`oneOf` flattening so `const` and vendor `x-*` keys can't leak back into the merged branch. (`internal/translator/schema.go`)
- **Provider 403 is now retryable** — Gemini/antigravity daily-quota errors can arrive as HTTP 403; these now trigger the connection fallback instead of a hard failure. (`internal/providers/providers.go`)
- **CodeBuddy CN stream cleanup** — The stall reader is now closed after the stream, stopping its shutdown watcher + stall timer (no per-request goroutine leak). (`internal/proxy/executor/codebuddy.go`)

### ⚙️ Graceful Shutdown Hardening

- New `internal/shutdown` package: a process-wide signal the first Ctrl+C / SIGTERM fires.
- `StallReader` now closes in-flight SSE upstream bodies on shutdown, so `server.Shutdown` drains streams in milliseconds instead of waiting out the 15s deadline — and the deferred DB/log-file close always runs.
- Translate-path SSE handlers emit a final `data: [DONE]` on abort so clients get a clean end instead of a truncated stream.
- A second Ctrl+C / SIGTERM force-quits immediately (stuck-drain escape hatch).
- Shutdown timeout logs a warning instead of `log.Fatalf`, so `conn.Close()` and the log file are still closed gracefully. (`internal/proxy/stall.go`, `cmd/9router-go/main.go`, `internal/handlers/chat/forward.go`, `internal/proxy/executor/openai.go`)

### 🔧 Internal

- Stream handlers now carry the request `ctx` and pull accumulated usage (incl. cached tokens) out of the translation session, so logged usage reflects real token counts instead of the character-estimate fallback.

## [v1.7.0] — 2026-08-06

### 🚀 New Executors

- **Trae SOLO remote agent** (`internal/proxy/executor/trae.go`) — Port of `open-sse/executors/trae.js`: `POST {base}/chat_sessions` creates a session, `GET {base}/chat_sessions/{id}/events` streams `plan_item` / `token_usage` / `done` as SSE. Cumulative `plan_item.thought` rendering (longest-wins per id, delta-only emission), `Cloud-IDE-JWT` auth, and `work`/`auto`/manual model modes. Non-stream requests aggregate into a single `chat.completion`. Round-trip test: `TestForwardTrae_StreamsAccumulatedThought`.
- **Windsurf gRPC-web** (`internal/proxy/executor/windsurf.go`) — Port of `open-sse/executors/windsurf.js`: hand-rolled protobuf `GetChatMessageRequest` encoder (Metadata.api_key + cascade_id + model_or_alias + repeated messages), gRPC-web framing (0x00 flag + big-endian length), and a `CompletionChunk` decoder (content / done+UsageStats / error) streaming OpenAI SSE. Catalog→wire model alias map ported verbatim; `crypto/rand` session/cascade ids. Non-stream requests aggregate frames into `chat.completion`. Round-trip test: `TestForwardWindsurf_StreamsGRPCWeb`.

### 🎙️ Xiaomi MiMo TTS

- `/v1/audio/speech` for the `xiaomi-mimo` provider now uses the chat-completions contract (port of `open-sse/handlers/ttsProviders/xiaomi-mimo.js`): target text in `role:assistant`, style/language instructions in `role:user`, voice via top-level `audio.voice`, base64 audio from `choices[0].message.audio.data`. (`internal/handlers/media/media.go`)

### ➕ Providers

- **tokenrouter** — Registered as an OpenAI-compatible upstream (`https://api.tokenrouter.com/v1/chat/completions`).

### 🗑️ Removed

- **qwen provider** — Removed from providers, OAuth config, and the alias map (deprecated upstream).

### 📋 Docs

- `TECHNICAL_DEBT.md` — windsurf + trae moved to resolved; zed + devin-cli documented with the safe-stopgap note (devin-cli corrected: ACP over **stdio** subprocess, not HTTP).

## [v1.6.1] — 2026-08-05

### 🐛 Bug Fixes

- **CodeBuddy CN 502** (`internal/proxy/executor/codebuddy.go`) — `codebuddy-cn` / `codebuddy-intl` now use a dedicated executor that forces `stream=true` upstream (CodeBuddy rejects non-stream with HTTP 400 code 11101), injects the CLI/IDE static headers, and re-aggregates OpenAI-chat SSE into a single `chat.completion` for non-stream clients (`sseToOpenAIJSON`, mirroring JS `parseSSEToOpenAIResponse`).
- Provider parity with the reference implementation.

## [v1.6.0] — 2026-08-04

### 🚀 Next.js Engine Feature Ports

- **TTS Voice Listing** (`/audio/voices`) — Full voice-listing with provider support (`edge-tts` default, `elevenlabs`, `gemini`, `local-device`), `?lang` filter, 24h in-process cache, and `byLang`/`languages` grouping matching the dashboard's media-providers page.
- **Proxy-Pools Deploy** (`/proxy-pools/{vercel,deno,cloudflare}-deploy`) — Deploy edge relay functions to Vercel/Deno/Cloudflare with status polling, plus a new `InsertProxyPool` DB method writing byte-compatible `data` JSON.
- **Headroom Management** (`/headroom/*`) — Full headroom-ai lifecycle in Go: binary/Python detection, spawn/stop/restart, compression extras install/uninstall, `/headroom/proxy` reverse proxy with SSRF guard, and dashboard HTML rewrite.
- **CLI-Tools Status** (`/cli-tools/all-statuses`) — Batch detection of 14 CLI tools (Claude, Codex, OpenCode, etc.) installed state + version.
- **Live Console Logs** (`/translator/console-logs`, `/stream`) — In-process ring buffer + SSE streaming of engine log output so the dashboard's "Monitor Console Log" shows Go logs live (25s keepalive, init/line/clear events).

### 🔍 Observability

- **Lightweight Request Tracing** (`/debug/traces`) — In-memory span recording + p50/p95/p99 latency per provider+model with `?n=` cap. Stdlib-only, no OpenTelemetry SDK dependency.

### 🛡️ Security

- **Prompt-Injection Guard** — Heuristic detection (`messages[]`, `input[]`, Claude content blocks) tagging classic injection attempts in logs. Toggle via `--no-injection-guard` / `INJECTION_GUARD_DISABLED` (on by default).
- **`/admin/health/reset` Moved Behind API-Key Auth** — Previously public; now requires a valid API key to prevent unauthenticated health-state resets (open-source hardening).
- **MITM Binds Loopback Only** — TLS proxy binds `127.0.0.1:443` instead of all interfaces, preventing LAN clients from using it as an open proxy.

### 🐛 Bug Fixes & Stability

- **SSE Fragment Rejoin** — Fixed `unexpected end of JSON input` on opencode free-tier by buffering/rejoining truncated SSE JSON payloads per session (1 MiB cap).
- **Codex/CommandCode Tool-Call Streams** — Stable per-call tool IDs/indices (using upstream `call_id`), correct `[DONE]` framing, and checked `w.Write` errors.
- **MITM Goroutine Leaks** — `Stop()` drains in-flight connections (WaitGroup + active conn close); request bodies bounded at 10 MiB.
- **Executor/OAuth Registry Mutexes** — Package-level registry maps now guarded by `sync.RWMutex` (race-free on re-registration).
- **Token Saver JSON Number Preservation** — `CompressMessages`/`InjectSystemPrompt` use `json.Number` so numeric fields (temperature, large ints) round-trip unchanged.
- **`interface{}` → `any`** — Lint cleanup across stream/log packages.

## [v1.5.0] — 2026-07-24

### 🚀 Architecture & Observability Enhancements

- **Modular `main.go` Refactoring** — Extracted CLI subcommands (`mitmEnable`, `mitmDisable`, `mitmStatus`, `resolveDataDir`) to `cmd/9router-go/commands.go` and encapsulated server routing setup into `handlers.SetupServerRouter()`.
- **Structured Request Logging Middleware** — Moved `statusWriter` and `RequestLogger` to `internal/middleware/logging.go`. Requests are logged with Correlation ID (`id=req_...`) using structured logger (`slog.Info`, `slog.Warn`, `slog.Error`).
- **Dynamic HTTP Status Log Levels** — Requests with status 5xx are logged at `ERROR` level, 4xx at `WARN` level, and 2xx/3xx at `INFO` level for clean log filtering in production.
- **Upstream Memory Exhaustion Protection** — Added `io.LimitReader` caps (1MB for upstream error bodies, 10MB for non-streaming completion bodies) to protect proxy memory from rogue upstreams.
- **Double WriteHeader Prevention** — Added `written bool` guard to `statusWriter` and `cw.IsCommitted()` checks across combo fallback handlers to eliminate `superfluous response.WriteHeader` warnings.
- **Typed Request ID Context Key** — Shared `log.RequestIDKey` across middleware and logging packages to ensure context lookups match reliably.

## [v1.4.0] — 2026-07-23

### 🛠️ Technical Debt Remediations (All 9 Items Resolved)

- **Context-based Per-Request Usage Capture** — Replaced global `translator.lastUsage` with context-captured isolation (`WithUsageCapture`, `SetUsage`, `GetAndClearUsage`) to eliminate cross-request data races under concurrent traffic. (`internal/translator/usage.go`)
- **Thread-safe Daily Usage Updates** — Protected `upsertDailyUsage()` with `dailyUsageMu` mutex to prevent concurrent SQLite read-modify-write races. (`internal/handlers/chat/usage.go`)
- **Committed Response Writer** — Wrapped `http.ResponseWriter` with `committedResponseWriter` to prevent safe-retry attempts after response headers have already been sent to the client. (`internal/handlers/chat/response_writer.go`)
- **Strict Context Propagation** — Replaced all `http.NewRequest` with `http.NewRequestWithContext` across handlers, proxy execution drivers, and OAuth helpers to prevent orphaned upstream connections.
- **Graceful Shutdown** — Implemented `http.Server` graceful shutdown with signal drain (15-second timeout) on SIGINT/SIGTERM. (`cmd/9router-go/main.go`)
- **SQLite Connection Pool Optimization** — Reduced SQLite `SetMaxOpenConns(4)` for optimal WAL mode performance and zero connection contention. (`internal/db/client.go`)
- **Thread-Safe ProxyPool Cache** — Added `sync.Map` `proxyPoolCache` in `internal/db/proxyPools.go` to preserve round-robin rotation indices across requests. (`internal/db/proxyPools.go`)
- **Unbounded Request Body Guard** — Added `middleware.MaxBody` (10MB limit) to protect all endpoints from OOM attacks. (`internal/middleware/max_body.go`, `cmd/9router-go/main.go`)

### ⚡ Metrics, Latency & Token Accounting Fixes

- **TTFT & Latency Tracking** — Added `StartTime` and `TTFT` tracking across all streaming and non-streaming proxy execution drivers (`openai`, `opencode`, `deepseek`, `claude`, `grok-cli`, `qoder`, etc.). (`internal/proxy/executor/`)
- **Input Token Calculation Fix** — Added `[]byte` type support to `CountValueChars` so fallback prompt token calculation accurately estimates token size instead of defaulting to 1 token. (`internal/handlers/chat/chat.go`)
- **Output Token Calculation Fix** — Connected `ResponseBuf` in `executor.Request` to record stream output tokens when upstream omits token usage objects. (`internal/proxy/executor/openai.go`)
- **Prompt Caching Tokens Support** — Updated `OpenAIUsage` to extract `cached_tokens` (`prompt_tokens_details.cached_tokens`) and `cache_creation_input_tokens`. (`internal/translator/types.go`, `internal/handlers/chat/usage.go`)

### 🧪 End-to-End Integration Test Suite

- **E2E Test Suite** — Added `internal/handlers/chat/e2e_integration_test.go` to test real HTTP streaming SSE, non-streaming JSON responses, TTFT latency, token accounting, and SQLite DB usage logging end-to-end.

### 🌐 Endpoints

- **`/api/hello`** — Registered `/api/hello` route returning `200 OK` for ping probes from Claude Code CLI. (`cmd/9router-go/main.go`)

## [v1.3.0] — 2026-07-22

### 🏥 Next.js-Compatible Health System

- **Connection-based health** — Replaced old `kv`-based `IsProviderHealthy`/`RecordProviderHealth` with `modelLock_*` fields in `providerConnections.data` JSON blob, matching Next.js `markAccountUnavailable` / `clearAccountError` flow. (`internal/db/health.go`, `internal/db/accounts.go`)
- **Per-connection model locks** — `LockConnectionModel` / `UnlockConnectionModel` / `IsConnectionModelLocked` use SQLite `json_set()` on shared `providerConnections.data`. Dashboard can read/write same fields. (`internal/db/accounts.go`)
- **`IsProviderAvailable`** — New `Repo` method checks if ANY connection for a provider has no active `modelLock_<model>`, replacing the old kv-based pre-check. (`internal/db/accounts.go`)
- **`POST /admin/health/reset`** — Resets `modelLock_*` on connections via query params `?provider=X&model=X`. Dashboard can call via headroom proxy. (`cmd/9router-go/main.go`)
- **Eliminated duplication** — Package-level `IsProviderHealthy` / `ResetProviderHealth` now delegate to `NewRepo(database)` instead of duplicating lock JSON parsing logic. (`internal/db/health.go`)

### 🧪 Test Fixes

- **False-pass assertions** — 3 handler tests were checking old kv-based `repo.IsModelLocked()` which always returned `false` vacuously. Changed to `repo.IsConnectionModelLocked(connID, model)` to actually verify connection-level locks. (`internal/handlers/chat_test.go`)

## [v1.2.0] — 2026-07-22

### 🎯 Gemini Tool Calling Fixes

- **thought_signature round-trip** — Gemini response encodes `thought_signature` into tool call `id` via `__ts__` separator; request decoder restores it for valid verification. Works for both streaming and non-streaming. (`internal/translator/gemini.go`)
- **Antigravity (AGY) support** — Custom `GeminiPart.UnmarshalJSON` handles `thoughtSignature` (camelCase) AND `thought_signature` (snake_case) since the internal `v1internal` endpoint returns camelCase. (`internal/translator/gemini.go`)
- **Tool response name fix** — `tool_call_id` with `__ts__` suffix no longer corrupts `functionResponse.name` extraction, preventing Gemini validation errors on turn 2. (`internal/translator/gemini.go`)

### 🎨 Logging

- **ANSI color-coded logs** — `INF` = green, `WRN` = yellow, `ERR` = red, `DBG` = cyan. Auto-detects TTY (disabled when piped). Disable via `NO_COLOR=1`. (`internal/log/log.go`)

### 🔧 Streaming Fixes

- **SSE multi-line** — Gemini stream chunks with multiple SSE lines (`data: ...\ndata: ...`) are now split and translated individually. Error on one line continues to next instead of aborting. (`internal/handlers/gemini_handler.go`)

### 🧹 Cleanup

- `fallback.go`: Removed misleading `WRN tokensaver failed` logs — replaced with idiomatic `if next, did := ...; did` pattern.
- `test_opencode.go`: Removed (stale temporary test file).
- `internal/translator/gemini_test.go`: Added (unit tests for `thought_signature` round-trip).

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
