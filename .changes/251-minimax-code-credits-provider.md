### feat(providers): MiniMax Code (mcode) credits provider

Ports upstream `decolua/9router` commit `85bc33ce` (MiniMax Code as a credits
provider) into 9router-go. MiniMax Code is a **new lane**, not an extension of
the existing API-key `minimax` provider: it authenticates through MiniMax's own
OAuth device flow and serves Anthropic Messages on the mavis gateway.

**What was added**

- `minimax-code` (China) and `minimax-code-global` (international) as separate
  providers. Sign-ins are per site, so they are never aliased onto each other
  (AGENTS.md §3.A); `mm` stays with the API-key `minimax` provider, the credits
  lane is published under `mmc`, and the global site keeps upstream's `mmg`.
- **OAuth device flow** (`internal/handlers/oauth/minimax_code.go`): PKCE S256
  against `{account}.minimax.{cn,io}`, including MiniMax's user-code variant
  (no `device_code` in the reply → the token endpoint is polled with the
  `user_code`, and `interval`/`expired_in` arrive in mixed units).
- **Token refresh** (`internal/proxy/oauth/minimax_code.go`): mcode refresh
  tokens are **single-use** — sending one twice answers `invalid_grant` and
  signs the account out. Two paths reach refresh (pre-dispatch expiry check and
  the executor's on-401 retry) and may hold different snapshots of one
  connection, so refreshes are collapsed in a singleflight keyed on the spent
  token value. Rotation is persisted; a reply without `expires_in` falls back to
  an hour rather than being stored as already-expired.
- **Executor** (`internal/proxy/executor/minimax_code.go`): forwards the Claude
  Messages wire with the OAuth bearer, the placeholder `x-api-key` that rides
  every mavis request, and per-session/timezone headers.
- **Quota normalization**: MiniMax Code reports credit exhaustion as **402/403
  with a body naming the balance** (English *and* CJK). Left at 402/403 the
  account loop would read it as an auth failure and re-spend a single-use
  refresh token per request instead of failing over; those are rewritten to
  **429 `rate_limit_error`**, which triggers combo fallback. A 403 that does not
  name the balance passes through untouched so the on-401 refresh path still
  sees a real auth refusal.
- **Usage tracking** (`internal/handlers/dashboard/usage_minimax_code.go`):
  credits balance, plan tier and M Plan rate windows read through mcode's signed
  account API (`yy` md5 + `x-signature`). A refused sign-in is reported in the
  wording the quota route's auth-expired check recognises, so it force-refreshes
  and retries once.
- **Claude-wire routing**: a provider whose registry entry declares
  `Format: "claude"` is now recognised as serving Anthropic Messages, so a
  `/v1/messages` client reaches it untranslated and a Chat Completions client
  gets the reply translated back. This is separate from the Anthropic-specific
  beta query, OAuth cloaking and beta-flag merge, which a third-party Messages
  endpoint does not want.
- **Dashboard**: catalog entries with artwork (no 404), device-flow
  registration, quota rendering, live catalogue import via a new
  `minimax-code` suggested-models filter, and connection probing against the
  gateway's model catalogue.

**Verification**

- `go test ./...` green; `-race` green on the touched packages.
- New offline tests, none of which reach a real MiniMax host (the site table is
  pinned to an `httptest` fixture):
  - refresh: rotation, expiry default, grant-dead classification on
    `invalid_grant`, no classification on 429/5xx, and concurrent refreshes of
    one token producing exactly **one** upstream send;
  - quota normalizer: the full 402/403/429/200 status-and-body matrix,
    including CJK bodies, the `base_resp` envelope, and pass-through of a 403
    that does not name the balance;
  - device flow: the S256 challenge really is the digest of the session
    verifier, the user-code variant posts `user_code`, and every
    `status:` envelope maps to the right outcome;
  - usage: signature recomputation, site separation, plan-window parsing,
    and the refused-sign-in wording;
  - registry: base URL, alias, auth shape, catalog, thinking levels and
    capabilities for both sites.
- **Integration** (`make test-integration`): the real router, real DB and a
  fake upstream — a `/v1/messages` client reaches the mavis endpoint
  untranslated with the bearer and the `x-api-key` placeholder, the Claude
  reply comes back Claude-shaped, a 402/403 credits refusal reaches the client
  as **429 `rate_limit_error`**, a 403 that does not name the balance keeps its
  own status, and both site ids route.
- `cd web && bun run build` green; `make vet-svelte` clean at baseline (83
  pre-existing type errors, 0 unresolved identifiers).

**Bugs the integration suite caught while porting** — all fixed here, all
observable to a client:

- `claudeNative` in `tryForwardWithConnection` only recognised Anthropic and a
  per-model Messages endpoint, so a provider whose *endpoint* speaks Messages
  was handed a body the handler had already converted. The two decisions now
  agree.
- The executor called the `handleClaudeMessages*` helpers unconditionally. Those
  helpers translate a Claude reply for a *non*-Claude client, so a Claude client
  on a Claude upstream had its answer rewritten into Chat Completions. The
  native case now relays the body untouched.
- `DoRequest` turns any non-200 into a typed error, so the credits refusal
  arrived as an error rather than a response to inspect; normalization runs on
  the error path.

**Intentional divergences from upstream**, recorded here per AGENTS.md §1:

- `mm` remains the alias of the API-key `minimax` provider and the China mcode
  site is published as `mmc`. Upstream reuses `mm` for the credits lane; doing
  the same here would move an existing provider's published prefix.
- The capability limits are the static catalog magpie's plugin ships; upstream
  marks its own figures as unverified by live probe.
