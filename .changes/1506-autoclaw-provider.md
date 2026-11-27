### feat(providers): add Autoclaw provider with one-time login

Adds `autoclaw` (alias `au`) as a first-class API-key provider serving Zhipu
AutoClaw's GLM models via the native desktop protocol at
`https://autoglm-api.autoglm.ai`. The six catalog ids are taken from the
installed AutoClaw 2.6.1 model feed: `zai_auto`, `zai_auto-fast`,
`zai_glm-x-preview` (GLM-5.4), `zaicoding_glm-5.3`, `zai_glm-5.3-flash`, and
`zai_glm-5.3-flashx-legal`.

**One-time login only.** The operator pastes `refresh_token|device_id|app_key`
into the connection form once. The `app_key` is the desktop client's signing
secret: it is **not** shipped with the gateway, so no third-party secret lives
in this tree. The app id (`100003`) and client version (`2.6.1`) are public
identifiers captured from the installed bundle and stay in the code.

The access token is then rotated automatically via `/userapi/v1/refresh`
(`internal/proxy/oauth/autoclaw.go`), lazily on the
request path and dedup-cached for 10s — the background OAuth refresher does
*not* select these connections (it only picks `authType: oauth` rows carrying
a `refreshToken` + `expiresAt`, while an Autoclaw row keeps its credential in
`apiKey`), so an idle connection is re-rotated on its next request rather than
proactively. No browser OAuth dance, no per-request re-login, and no DeepSeek
connection are required.

**Refresh-token rotation.** The installed desktop client treats a response
without a new `refresh_token` as a failure, so this refresher does too. A
primary `/userapi/v1/refresh` reply with business code `400002` falls back to
`/userapi/v1/agent-refresh`, matching the client's `r1n.refresh` flow. The
rotated token is returned in `TokenResult`; the request-path executor has no
DB handle, so unlike a background OAuth row it cannot persist the new value
back to the connection. A real rotation therefore eventually needs the
operator to re-paste the credential once. A background persistence lane is
not in scope for this PR.

**Wire contract.** Each request is signed with the app's MD5 envelope
(`x-auth-sign = md5("100003&<ts>&<app_key>")`, where `app_key` is the
operator's credential field), the model rides in
`X-Request-Model` (not the body), and `stream: true` is forced when the
client asked for a stream. A 401 triggers one forced refresh + retry against
the same endpoint: the executor calls `SendOnce` (not `DoRequest`), because
`DoRequest` wraps every non-200 in an `*UpstreamError`, which made the 401
branch unreachable on the first head; any non-200 that survives the retry is
surfaced as an `*UpstreamError` so failover classifies it, never relayed as a
200 SSE stream. The relay is per-chunk flushed (`proxy.SSECopy`), so the first
frame reaches the client as soon as upstream sends it instead of when the
stream closes. The upstream protocol is SSE-only, so it answers a
`stream:false` ask with frames too: the non-stream lane folds them into one
`chat.completion` with the shared `sseToOpenAIJSON` helper before labelling
the body `application/json` (raw `data:` frames under a JSON header are
unparseable for every OpenAI client), and an SSE body carrying no completion
chunk answers 502 (`NoCompletionInStream`) so combo fallback can move on.

**Verification (offline, no credentials).**
- `internal/proxy/oauth/autoclaw_test.go` — refresh round-trip against an
  httptest fake; rejects empty refresh token.
- `internal/proxy/executor/autoclaw_test.go` — signer header determinism
  (fixed timestamp → exact MD5), body forces `stream: true` only when the
  client asked for a stream, executor sends `X-Request-Model` and relays the
  SSE stream, and the 401 path: first chat call 401s, the executor
  force-refreshes once, the retry hits the same overridable base URL, and the
  second call succeeds (`TestAutoclawExecutor401RetryHitsOverrideBaseURL`).
  `TestAutoclawExecutorNonStreamFoldsSSEIntoJSON` — a `stream:false` request
  is answered with one aggregated `chat.completion` (content `hello world`,
  `finish_reason` `stop`, usage preserved) under `application/json`, never
  raw `data:` frames. `TestAutoclawExecutorStreamFlushesBeforeUpstreamCloses`
  — a real HTTP hop where the first frame lands while the upstream still
  holds the stream open (TTFB != stream duration).
  `TestAutoclawExecutorSignsWithOperatorSuppliedAppKey` — the request is
  signed with the operator's `app_key`, never with a key from this tree.
  `TestAutoclawExecutorRejectsMissingAppKey` — a credential without the
  `app_key` is refused with the expected format and never reaches upstream.
  Both fail on the head that still embedded the app key (the signature there
  is the embedded one, and a credential without a key was signed anyway).
- `internal/providers/catalog_sweep_test.go` — all six autoclaw ids pinned
  to the 1M context window verified from the app's catalog snapshot.
- `internal/integration/autoclaw_live_test.go` — live end-to-end through the
  real router and database: a fake upstream mimics the 2.6.1 protocol
  (signed headers, refresh-token rotation on every refresh, SSE-only chat).
  It pins `X-Request-Model` from the body, `X-Version: 2.6.1`, the refresh
  body fields, and a parseable `chat.completion` at status 200.
- `TestAutoclawExecutorHonoursConnectionBaseURL` — a saved connection's
  `data.baseUrl` is used for both refresh and chat (the dispatcher's
  `/v1/chat/completions` suffix is stripped), so a custom node works.

**Capability source (REA-verified).** The six ids, names, and limits are taken
from the installed AutoClaw 2.6.1 client's signed model-catalog snapshot
(`runtime/model-catalog/remote-snapshot.json`), read statically via REA. Every
entry reports `contextWindow: 1048576`; `maxTokens` is 131072 (`zai_auto`,
`zai_glm-5.3-flash`), 393216 (`zai_auto-fast`), or 307200 (the three GLM-5.3
entries). Capabilities therefore come from the provider's own feed rather
than the 128k floor. The signing scheme was also verified against the app
bundle: `md5(appId&ts&appKey)` with `appId = 100003` and the refresh body
`{refresh_token, source_id:"autoclaw", device_id}`.

**Maintenance note.** `APP_ID` and `X-Version` are captured from desktop client
build 2.6.1. If AutoClaw bumps the build, every signed request may 403 until
the version constant is bumped and the operator re-pastes the current
`app_key`. `X-Tm: win` is the working fallback; some accounts refuse `linux`.
