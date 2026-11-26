### feat(providers): add Autoclaw provider with one-time login

Adds `autoclaw` (alias `au`) as a first-class API-key provider serving Zhipu
AutoClaw's GLM models (GLM-5.3, GLM-5-Turbo, GLM-5.3-Flash, DeepSeek-V4) via
the native desktop protocol at `https://autoglm-api.autoglm.ai`.

**One-time login only.** The operator pastes `refresh_token|device_id` into the
connection form once (copied from the AutoClaw app, glmproxy, or autoclawpi
console). The gateway rotates the access token in the background via
`/userapi/v1/refresh` (`internal/proxy/oauth/autoclaw.go`), so no browser OAuth
dance, no per-request re-login, and no DeepSeek connection are required.

**Wire contract.** Each request is signed with the app's MD5 envelope
(`x-auth-sign = md5("100003&<ts>&<APP_KEY>")`), the model rides in
`X-Request-Model` (not the body), and `stream: true` is forced. A 401 triggers
one forced refresh + retry.

**Verification (offline, no credentials).**
- `internal/proxy/oauth/autoclaw_test.go` — refresh round-trip against an
  httptest fake; rejects empty refresh token.
- `internal/proxy/executor/autoclaw_test.go` — signer header determinism
  (fixed timestamp → exact MD5), body forces `stream: true`, executor sends
  `X-Request-Model` and relays the SSE stream.
- `internal/providers/catalog_sweep_test.go` — autoclaw models pinned to their
  published windows (1M / 200k), no floor hits.

**Maintenance note.** `APP_ID`/`APP_KEY`/`X-Version` are captured from desktop
client build 1.18.5. If Autoclaw bumps the build, every signed request 403s
until the constants are re-dumped. `X-Tm: win` is the working fallback; some
accounts refuse `linux`.
