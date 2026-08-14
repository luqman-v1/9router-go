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

- **CodeBuddy OAuth Configuration**: `codebuddy-cn` and `codebuddy-intl` OAuth token refresh configuration registered in `KnownOAuthConfigs` (`internal/providers/oauth.go`), alongside its dedicated stream executor (`internal/proxy/executor/codebuddy.go`).
- **Realtime SSE Usage Stream & Topology Animation**: Added `internal/usagetracker` with in-memory active request tracking and SSE broadcast handlers (`/api/usage/stream` & `/usage/stream`) matching Next.js dashboard topology animation requirements.
- **Edge Relay URL Rewriting**: Added automatic `BaseURL` rewrite and `x-relay-target` / `x-relay-path` header injection for Vercel, Cloudflare, and Deno edge relays in `GetProviderConfig`.

- **Combo/router fallback bypasses model-lock backoff on 429 → antigravity rate-limit loop** (was Critical): `handleComboFallback` / `handleMessagesComboFallback` (`internal/handlers/chat/combo.go`) used to call `tryForwardWithConnection` directly, so `LockConnectionModel` was never invoked on retryable errors — unlike the single-model path `handleAccountFallback` (`fallback.go:97`) — leaving the exponential 429 backoff dead in the router path: every request re-tried all combo models back-to-back on the same connection/account, got 429, returned 429, and the client's ~35s retry repeated the loop forever (log signature: two `[fallback] upstream failed ... status=429` lines, different models, same `conn=`, under one `[request] POST /messages status=429`). Fix: new `comboLockRetryable` helper (`combo.go:554`) runs on every `RetryableStatusCodes` error in both combo loops — it classifies via `ClassifyError`, calls `LockConnectionModel(connID, model, cooldownSec, newBackoffLevel)` so the exponential backoff persists across requests, and appends the conn to a request-local `excludeIDs` passed into `getBestConnection` so the remaining combo models don't re-select the same connection (same account = same quota bucket). A locked-connection skip covers pinned connections whose direct-fetch branch bypasses `getBestConnection`'s lock check; `context.Background()` → `ctx` in `handleComboFallback` so client cancels propagate; the 502/503/504 transient-wait sleep is preserved. Test: `TestHandleMessagesComboFallback_429LocksAndExcludesConnection` (`fallback_test.go`) asserts a 429 locks the connection AND keeps the second combo model from re-hitting it (exactly 1 upstream hit).
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
- **SSE Streaming Broken by Missing `http.Flusher` in Logging Middleware**: Added a `Flush()` method to `statusWriter` in `internal/middleware/logging.go` that forwards to the underlying writer. Streaming handlers' `w.(http.Flusher)` assertions now succeed → mid-stream flush restored.
- **`StreamMetrics.ResponseBuf` Unbounded Accumulation**: Replaced `strings.Builder` with a capped `ResponseBuf` type (`internal/handlers/shared/types.go`) that drops content past 100,000 bytes while still implementing `io.Writer`.
- **`ScanStream` Splits Multi-Line / CRLF SSE Events**: Rewrote `internal/proxy/sse_scanner.go` as a proper SSE event accumulator — joins `data:` fields per event, fires on blank-line delimiter, strips one optional space, skips keep-alives, handles CRLF. Added `TestScanStreamAccumulatesEvents`.
- **EventStream Frame `headerLen` Not Validated → Slice Panic**: Added bounds check `int(headerLen) > int(totalLen)-12 → error` in `internal/providers/eventstream.go` before slicing.
- **`SSECopy` Swallows Read Errors & Returns nil**: `internal/proxy/sse.go` now propagates the upstream read error (nil only on `io.EOF`) and surfaces client write errors. `ReadBody` capped with `io.LimitReader` (10MB).
- **`StallReader` Timer Race & Leak**: `internal/proxy/stall.go` `Close()` is now idempotent and serialized with the timer goroutine via the shared `sync.Once`; the existing `defer bodyCloser.Close()` call sites now stop the timer cleanly on normal completion.
- **HTTP Client Has No Timeout (`Timeout: 0`)**: `NewChatHandler` and `NewMediaHandler` now use a cloned transport with `ResponseHeaderTimeout = 2m`, closing the "accept then silent" gap without cutting long SSE streams (`Timeout: 0` retained for body).
- **SQLite DB File World-Readable, Stores Cleartext API Keys**: `internal/db/client.go` now `os.Chmod`s the DB dir to `0700` and the DB file to `0600` on every open.
- **Data Race on Shared `ProxyPool` Cache**: `internal/db/proxyPools.go` no longer mutates cached `*ProxyPool` in place — fresh reads go through `LoadOrStore` and the stored value is immutable.
- **Global `translator.states` Map Collision Across Concurrent Streams** (was Critical): Added `TranslateOpenAIToClaudeStreamSession(sessionKey, chunk)` which keys translation state by a per-request session key instead of `chunk.ID`; all three production call sites (`forward.go`, `gemini_handler.go`, `executor/openai.go`) generate a unique key per stream and `defer translator.ClearStreamState(key)`.
- **SSRF Guard Is Dead Code**: `AssertPublicURL` now resolves the hostname and checks the resolved IPs (loopback/private/link-local/unique-local) via `net.LookupIP`, closing the DNS-rebinding gap. Decision: it is intentionally NOT wired globally into `DoRequest` — a universal guard rejects legitimate localhost/mock upstreams (confirmed by breaking tests) and static provider config is not user-controlled; SSRF surface on the provider-select routes is already an enum whitelist. Review the decision if provider `BaseURL` ever becomes user-writable.
- **JSON Path Injection via Model Name in `json_set`**: `modelLockDataKey` now emits a quoted path segment (`"$.\"modelLock_"+model+"\""`) so `.`/`"`/`[`/`]` in a model name is treated as a literal key.
- **OAuth Import Connection IDs Collide**: All three connection-ID constructions in `internal/handlers/oauth/oauth.go` (import, kiro-social, codex bulk) now use `randomString(12)` instead of `len(credential)%10000`.
- **Shell Injection Latent in MITM `/etc/hosts` Editing**: `AddHostsEntries`/`RemoveHostsEntries` in `internal/mitm/dns.go` no longer use `sh -c` interpolation — they call `exec.Command("sudo", "tee"/"sed", ...)` directly; dead `runSudo` removed.
- **Path Traversal Latent in Leaf Cert Cache**: `GetOrCreateLeafCert` validates the SNI domain against `[a-zA-Z0-9._-]` (≤253 chars) before building paths.
- **Cert-Cache Data Race / TOCTOU**: `GetOrCreateLeafCert` serializes per-domain via a `leafLocks` mutex map (double-checked), preventing concurrent handshakes from generating conflicting certs.
- **OAuth Refresh Errors Embed Full Upstream Body**: `proxy/oauth/standard.go` (helper `truncateBody`, 200-char cap), `xai.go`, `claude.go`, `providers/oauth.go`, and `auth/token.go` now truncate upstream bodies in error messages so echoed refresh tokens can't leak into logs.
- **Query-String API Keys Leak**: `ExtractApiKey` now accepts only `Authorization: Bearer` and `X-API-Key`; `?key`/`?api_key`/`?apiKey` are rejected. Auth tests updated accordingly.
- **Unbounded `io.ReadAll` Without LimitReader**: Error bodies in `proxy/proxy.go`, `proxy/gemini.go`, `proxy/executor/providers.go` capped at 1MB; non-stream `jsonResponse`/`geminiNonStream` capped at 10MB.
- **Fallback Secrets in Config**: `INITIAL_PASSWORD` no longer defaults to `"123456"` (empty forces explicit set); JWT secret falls back to `""` + error log (never a static secret) if `crypto/rand` fails. Config test updated.
- **`.env` Parsing Ignores Quotes & Comments**: `loadDotenv` now strips single/double quotes and inline `#` comments (outside quotes) and never overrides existing env vars.
- **`lastUsage` Global Poisoning**: Removed `SetLastUsage` writes from the streaming translate path (per-chunk, zero-choice, and finish blocks) and from non-stream `TranslateOpenAIToClaude`/`TranslateGeminiChunkToOpenAI`. Stream state is now retained (not deleted on finish) so callers read usage via `GetStreamUsage(sessionKey)`; cleanup is the caller's `defer ClearStreamState`. Translator tests updated.
- **`committedResponseWriter` Unused**: Was already wired in `internal/handlers/chat/combo.go` (lines 244/380/541) with `IsCommitted()` guards — stale TODO comment in `response_writer.go` removed.
- **`INSERT OR REPLACE` Daily Usage Lost Updates**: Documented as single-writer in `internal/db/usage.go`. The daily aggregation is a pre-merged JSON blob; safe atomic merge requires a schema change, so a `ponytail:` note now declares the multi-process limitation rather than a half-fix.
- **Codex/Commandcode Tool-Call Stream Issues**: `internal/proxy/executor/stream.go` now carries the real upstream `call_id` (stable per tool call via `toolCallIndex`), `.done` and `tool-input-delta` emit matching `id`+`index`, `[DONE]` is emitted exactly once (finish chunk via `writeSSEFinish` when upstream omits `response.completed`), and all `w.Write` errors are checked. 3 new tests.
- **MITM Server / Proxy Bind on All Interfaces**: `internal/mitm/server.go` binds `127.0.0.1:443` instead of `:443` — LAN clients can no longer use the unauthenticated local proxy as an open proxy.
- **Goroutine Leaks in MITM Lifecycle**: `acceptLoop` tracks `handleConn` via `sync.WaitGroup` + `active` conn map; `Stop()` closes live connections (so blocked reads return) then waits for in-flight handlers; `io.ReadAll(req.Body)` bounded to 10 MiB (over-limit connections dropped, not silently truncated). Race-clean.
- **Registry Maps Unsynchronized**: `internal/proxy/oauth/refresher.go` and `internal/proxy/executor/executor.go` registry maps guarded by `sync.RWMutex` (Register = write, Get = read). `sync.Once` rejected because tests deliberately re-register to override. Race-clean.
- **Token Saver Re-Marshal Corrupts Field Types**: `CompressMessages` and `InjectSystemPrompt` now decode via `unmarshalAny` (`json.Decoder.UseNumber()`), preserving numeric literals as `json.Number` instead of coercing to `float64` — large ints and floats round-trip unchanged. New test asserts `9007199254740993` survives.
- **SSE Fragment Rejoin (opencode free-tier)**: `TranslateOpenAIToClaudeStreamSession` buffers a truncated SSE JSON payload per session (`pendingJSON`, capped 1 MiB) and rejoins it with the continuation chunk, instead of erroring `unexpected end of JSON input` and dropping the chunk. Malformed (non-truncation) JSON still errors. 2 new tests.
- **Next→Go Engine Ports (COMPARISON.md)**: TTS voices (`voices.go`), proxy-pools deploy Vercel/Deno/Cloudflare (`deploy.go`), headroom process management (`internal/headroom/`), cli-tools all-statuses (`clitools.go`) — ported into the Go engine so the shared Next dashboard keeps working. Feature-parity mapping in `COMPARISON.md`.
- **CodeBuddy CN 502 (GitHub #1)**: `codebuddy-cn`/`codebuddy-intl` now use a dedicated executor (`internal/proxy/executor/codebuddy.go`) that forces `stream=true` upstream (CodeBuddy rejects non-stream, HTTP 400 code 11101), injects the CLI/IDE static headers, and re-aggregates OpenAI-chat SSE into a single JSON `chat.completion` for non-stream clients (`sseToOpenAIJSON`, mirroring the JS `parseSSEToOpenAIResponse`). 8 executor tests pass.
- **Trae SOLO remote agent executor**: `trae` no longer falls back to `ForwardOpenAI` — it uses `ForwardTrae` (`internal/proxy/executor/trae.go`), a port of `open-sse/executors/trae.js`: `POST {base}/chat_sessions` then `GET {base}/chat_sessions/{id}/events` SSE, rendering cumulative `plan_item.thought` (longest-wins) as OpenAI chunks, with `Cloud-IDE-JWT` auth and `work`/`auto`/manual model modes. `providerSpecificData` identity fields in `common_params` fall back to reference defaults (not carried by the executor `Request`). Round-trip test: `TestForwardTrae_StreamsAccumulatedThought`.
- **Windsurf gRPC-web executor**: `windsurf` no longer falls back to `ForwardOpenAI` — it uses `ForwardWindsurf` (`internal/proxy/executor/windsurf.go`), a port of `open-sse/executors/windsurf.js`: hand-rolled protobuf `GetChatMessageRequest` encoder (Metadata.api_key + cascade_id + model_or_alias + messages), gRPC-web framing (0x00 flag + big-endian length), and a `CompletionChunk` decoder (content / done+UsageStats / error) that streams OpenAI SSE. Catalog→wire model alias map ported verbatim; `crypto/rand` session/cascade ids; non-stream requests aggregate frames into a single `chat.completion`. Round-trip test: `TestForwardWindsurf_StreamsGRPCWeb`.
