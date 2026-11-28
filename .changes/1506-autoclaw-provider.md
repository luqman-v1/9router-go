### feat(providers): add Autoclaw provider with OAuth login

Adds `autoclaw` (alias `au`) as an OAuth-only provider serving Zhipu AutoClaw's
GLM models via its native desktop protocol at `https://autoglm-api.autoglm.ai`.
The six catalog ids come from the installed AutoClaw 2.6.1 model feed:
`zai_auto`, `zai_auto-fast`, `zai_glm-x-preview` (GLM-5.4), `zaicoding_glm-5.3`,
`zai_glm-5.3-flash`, and `zai_glm-5.3-flashx-legal`.

**OAuth login, no API key field.** The dashboard offers an OAuth login for this
provider (the same surface zcode uses), backed by the desktop client's own
Z.ai flow: `POST /userapi/overseasv1/zai-oauth-url` returns the `chat.z.ai`
authorization URL, and `POST /userapi/overseasv1/zai-oauth-login` exchanges
`{code,state,navigate_uri}` for `{user_id,access_token,refresh_token}`. The
handler stores one connection carrying the refresh token and device id; no
credential is pasted by hand. The app id (`100003`), client version (`2.6.1`),
and signing key (`KNc.appKey` from build 2.6.1) are client identifiers captured
from the bundle and live in the code — the upstream client signs every model
request with them, so an OAuth token alone cannot be used without them.

**Token rotation.** The access token is refreshed via `/userapi/v1/refresh`
(`internal/proxy/oauth/autoclaw.go`), lazily on the request path and
dedup-cached for 10s. The background OAuth refresher does not select these rows
(it picks `authType: oauth` rows with `refreshToken` + `expiresAt`, while an
Autoclaw row keeps its durable credential in `apiKey`), so an idle connection
is re-rotated on its next request. The desktop client requires a fresh
`refresh_token` on every rotation, which this refresher enforces; a business
code `400002` falls back to `/userapi/v1/agent-refresh` as the client does.
The rotated pair is persisted back to the connection by the chat handler via
the executor's `PersistCredentials` callback, so the next request after the
dedup cache expires does not replay an already-consumed token.

**Connection identity.** The OAuth login response carries only `user_id`.
After the exchange the handler calls `/userapi/v1/user-profile` (same signed
identity-gateway envelope, token in `Authorization`) and labels the connection
with the account email; the email is also stored in the connection data.

**OAuth loopback.** chat.z.ai accepts only the redirect URI registered for the
AutoClaw OAuth client, `http://localhost:18432/auth/callback-zai`. The gateway
binds that loopback during login and relays the callback into the dashboard's
`/api/oauth/callback-result` poller. The Aliyun captcha SDK runs in the browser
(silent risk-based or visible slider) and the resulting proof is forwarded as
`ali_captcha_verify_param`; without it `zai-oauth-url` answers the misleading
`631002` "version unsupported".


**Wire contract.** The header set mirrors a request captured from AutoClaw
2.6.1 with a local MITM proxy: `X-Authorization` (not `Authorization`),
`X-Agent-Id: main`, `user-agent: node`, `x-return-billing: true`,
`x_trace_id: autoclaw-desktop`, `accept: application/json`, the MD5 envelope
(`x-auth-sign = md5("100003&<ts>&<app_key>")`), and the rest of the captured
desktop headers. The token already carries the `Bearer ` prefix upstream, so
the executor does not add a second one.

**Body shape (MITM-verified).** The managed gateway rejects a bare OpenAI body
with an empty 406. A capture of a real AutoClaw 2.6.1 chat showed the required
shape: `stream: true`, `stream_options.include_usage: true`, `store: false`,
`max_completion_tokens`, `reasoning_effort: "max"`, and — critically — a leading
`system` message carrying AutoClaw's exact 7.5 KB system prompt. A minimal
generic system prompt is rejected too; upstream validates the prompt itself.
`autoclawBody` normalises every request to that shape and the captured prompt
lives in `autoclaw_system_const.go`. Caller-supplied values win: `max_tokens`
is remapped to `max_completion_tokens`, and the desktop prompt is always
prepended as the FIRST system message even when the caller supplies its own
(OpenCode's title-generation prompt otherwise answers 406 with
`data: \n\ndata: [DONE]`); the client's system prompt is kept as the next
message. The upstream is SSE-only, so a non-stream client's request is still
streamed upstream and folded into one `chat.completion` with `sseToOpenAIJSON`
before being labelled JSON; an SSE body with no completion chunk answers 502
(`NoCompletionInStream`).

The model rides in `X-Request-Model` (not the body). A 401 triggers one forced
refresh + retry through `proxy.SendOnce`; any non-200 surviving the retry
surfaces as an `*UpstreamError` so failover sees the real status. The relay is
per-chunk flushed (`proxy.SSECopy`).


**Usage metrics.** The stream path relays upstream SSE with `proxy.SSECopy`;
that helper does not record token usage by itself, so the dashboard's Speed,
Completion and Cached columns stayed at 0 even though AutoClaw's final chunk
carried a full `usage` object. `ForwardAutoclaw` now feeds each SSE chunk to
`captureUsageFromSSEChunk` in the TTFT callback and stores the result in the
request context, the same path the generic OpenAI executor uses. A live stream
request then logged `prompt_tokens:1957, completion_tokens:2, cached_tokens:1792`
instead of all zeroes.

**Verification (offline, no credentials).**
- `internal/handlers/oauth/autoclaw_test.go` — authorize round-trip against an
  httptest fake; pins the `zai-oauth-url` request and the returned auth URL.
- `TestAutoclawCaptchaConfigProofAndRedirectURI` pins the captcha config
  endpoint, proof forwarding, and the registered loopback redirect URI.
- `TestAutoclawUserEmail` pins the post-login profile call that labels the
  connection with the account email.
- `internal/proxy/oauth/autoclaw_test.go` — refresh round-trip, agent-refresh
  fallback on `400002`, and rejection of a response without a rotated
  `refresh_token`.
- `internal/proxy/executor/autoclaw_test.go` — signer determinism, body stream
  flag, `X-Request-Model` from the body, 401 retry on the overridable base URL,
  non-stream SSE fold, per-chunk flush, and connection `data.baseUrl` honoured
  for both refresh and chat, and persistence of the rotated refresh token
  through `PersistCredentials`.
- `internal/integration/autoclaw_live_test.go` — live end-to-end through the
  real router and database with a fake upstream that rotates the refresh token
  and serves SSE; pins signed headers, the model header, the refresh body, and
  a parseable `chat.completion` at status 200.
- `internal/providers/catalog_sweep_test.go` — all six ids pinned to the 1M
  context window verified from the app's catalog snapshot.
- `web/e2e/autoclawProviderModels.test.ts` — the dashboard renders all six
  models and the catalog exposes `autoclaw` as OAuth-only (`authModes: [oauth]`).

**Live verification.** With a real OAuth connection and the running gateway,
`POST /api/models/test {"model":"autoclaw/zai_auto","kind":"llm"}` went from
`{"status":406}` to `{"ok":true,"status":200}` after the body-shape fix. The
system prompt and required fields were recovered from a local MITM capture of
the desktop client; the capture and credentials were deleted afterwards.


**Provider logo.** `web/public/providers/autoclaw.png` is the official AutoClaw
app icon extracted from AutoClaw 2.6.1 (white glyph on dark rounded square),
replacing the initial "AC" placeholder. The Svelte bundle was rebuilt so the
embedded `web/dist` carries the corrected icon.

**Capability source (REA-verified).** The six ids, names, and limits come from
the installed AutoClaw 2.6.1 client's model-catalog snapshot, read statically
via REA. Every entry reports `contextWindow: 1048576`; `maxTokens` is 131072
(`zai_auto`, `zai_glm-5.3-flash`), 393216 (`zai_auto-fast`), or 307200 (the
three GLM-5.3 entries). The signing scheme was verified against the app bundle:
`md5(appId&ts&appKey)` with `appId = 100003` and the refresh body
`{refresh_token, source_id:"autoclaw", device_id}`.

**Maintenance note.** `APP_ID`, `X-Version`, and the signing key are captured
from desktop client build 2.6.1. If AutoClaw bumps the build, the constants
must be re-dumped. `X-Tm: win` is the working fallback; some accounts refuse
`linux`.
