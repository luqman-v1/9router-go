### feat(providers): add Autoclaw provider with one-time login

Adds `autoclaw` (alias `au`) as a first-class API-key provider serving Zhipu
AutoClaw's GLM models (GLM-5.3, GLM-5-Turbo, GLM-5.3-Flash, DeepSeek-V4) via
the native desktop protocol at `https://autoglm-api.autoglm.ai`.

**One-time login only.** The operator pastes `refresh_token|device_id` into the
connection form once (copied from the AutoClaw app, glmproxy, or autoclawpi
console). The access token is then rotated automatically via
`/userapi/v1/refresh` (`internal/proxy/oauth/autoclaw.go`), lazily on the
request path and dedup-cached for 10s — the background OAuth refresher does
*not* select these connections (it only picks `authType: oauth` rows carrying
a `refreshToken` + `expiresAt`, while an Autoclaw row keeps its credential in
`apiKey`), so an idle connection is re-rotated on its next request rather than
proactively. No browser OAuth dance, no per-request re-login, and no DeepSeek
connection are required.

**Refresh-token rotation (unverified against a live login).** The refresher
assumes the upstream echoes the same `refresh_token`: it keeps the stored one
when the response omits the field. If AutoClaw actually rotates the refresh
token, the rotated value is not persisted back to the connection (no executor
owns a DB write on the request path), and the operator must re-paste
`refresh_token|device_id` once the stored token is invalidated. Verify the
real behavior with a live login before relying on long-lived connections.

**Wire contract.** Each request is signed with the app's MD5 envelope
(`x-auth-sign = md5("100003&<ts>&<APP_KEY>")`), the model rides in
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
- `internal/providers/catalog_sweep_test.go` — autoclaw models pinned to their
  published windows (1M / 200k), no floor hits.

**Capability source.** The seven pinned windows are cross-checked against
public vendor pages for the underlying models: GLM-5.3 is documented at 1M
context / 128K max output
([docs.z.ai/guides/llm/glm-5.3](https://docs.z.ai/guides/llm/glm-5.3)); the
GLM-5 family page documents 200K context / 128K max output
([docs.z.ai/guides/llm/glm-5](https://docs.z.ai/guides/llm/glm-5)), which is
the spec the `zai_glm-5-turbo` pin (200k/131k) follows; GLM-5.3-Flash is
listed at 1,048,576 context / 131,072 completion tokens
([openrouter.ai/z-ai/glm-5.3-flash](https://openrouter.ai/z-ai/glm-5.3-flash));
DeepSeek-V4-Pro and DeepSeek-V4-Flash are documented at 1M context with a
393,216 output ceiling
([api-docs.deepseek.com/news/news260424](https://api-docs.deepseek.com/news/news260424),
[fast.io/resources/deepseek-token-limit](https://fast.io/resources/deepseek-token-limit)).
The `zai_auto` / `zai_auto-fast` routing ids ride the same GLM-5.3 family, so
their windows follow the GLM-5.3 pin. What no vendor page covers is the
AutoClaw id mapping itself: the `tdpsk_*` / `zaicoding_*` ids and their
windows come from the community-proxy catalogs that front AutoClaw (glmproxy
and autoclawpi publish the same ids and windows). Autoclaw's own model feed
requires a signed session, so there is no unauthenticated `/v1/models`
endpoint in-tree to verify against, and no models.dev entry exists for this
private app protocol. The pins are effective-value checks
(`GetCapabilitiesDetailForModel` returns the pinned values, not the 128k
floor) but are not sourced from a live Autoclaw feed — verify against a real
`refresh_token|device_id` login before trusting them in production.

**Maintenance note.** `APP_ID`/`APP_KEY`/`X-Version` are captured from desktop
client build 1.18.5. If Autoclaw bumps the build, every signed request 403s
until the constants are re-dumped. `X-Tm: win` is the working fallback; some
accounts refuse `linux`.
