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
The rotated token is returned in `TokenResult`; the request-path executor has
no DB handle, so it cannot persist the new value back to the connection. A
background persistence lane is out of scope.

**Wire contract.** Each request is signed with the app's MD5 envelope
(`x-auth-sign = md5("100003&<ts>&<app_key>")`), the model rides in
`X-Request-Model` (not the body), and `stream: true` is forced only when the
client asked for a stream. A 401 triggers one forced refresh + retry through
`proxy.SendOnce`; any non-200 surviving the retry surfaces as an
`*UpstreamError` so failover sees the real status. The relay is per-chunk
flushed (`proxy.SSECopy`). The upstream is SSE-only, so a `stream:false` ask is
folded into one `chat.completion` with the shared `sseToOpenAIJSON` helper
before being labelled `application/json`; an SSE body with no completion chunk
answers 502 (`NoCompletionInStream`).

**Verification (offline, no credentials).**
- `internal/handlers/oauth/autoclaw_test.go` — authorize round-trip against an
  httptest fake; pins the `zai-oauth-url` request and the returned auth URL.
- `internal/proxy/oauth/autoclaw_test.go` — refresh round-trip, agent-refresh
  fallback on `400002`, and rejection of a response without a rotated
  `refresh_token`.
- `internal/proxy/executor/autoclaw_test.go` — signer determinism, body stream
  flag, `X-Request-Model` from the body, 401 retry on the overridable base URL,
  non-stream SSE fold, per-chunk flush, and connection `data.baseUrl` honoured
  for both refresh and chat.
- `internal/integration/autoclaw_live_test.go` — live end-to-end through the
  real router and database with a fake upstream that rotates the refresh token
  and serves SSE; pins signed headers, the model header, the refresh body, and
  a parseable `chat.completion` at status 200.
- `internal/providers/catalog_sweep_test.go` — all six ids pinned to the 1M
  context window verified from the app's catalog snapshot.
- `web/e2e/autoclawProviderModels.test.ts` — the dashboard renders all six
  models and the catalog exposes `autoclaw` as OAuth-only (`authModes: [oauth]`).

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
