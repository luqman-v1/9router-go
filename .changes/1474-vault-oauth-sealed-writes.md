### fix(oauth): seal credentials in the four OAuth write paths

Four OAuth handlers wrote `providerConnections.data` through `RawDB().Exec`, so
with `ROUTER_MASTER_KEY` set access and refresh tokens landed in `data` in the
clear (breaking the vault's "never plaintext at rest" guarantee), and the
paired `SELECT data` read-back returned ciphertext that `json.Unmarshal`
silently rejected — dropping the refresh token on relogin/rotation and logging
the user out.

`HandleAuthCodeExchange`, `completePKCEExchange`, `saveDeviceConnection` and
`storeGoogleConnection` now write through `ReplaceProviderConnectionPayload` /
`CreateProviderConnectionFull`, which seal credentials before the write and
hydrate them on read. A vault-less install is unchanged (data passes through
untouched, no extra statement).

**Scope, stated plainly:** this closes those four paths only. Credentials are
still written plaintext on other paths — the Codex bulk import
(`internal/handlers/oauth/oauth.go`), the Cline OAuth update/insert
(`internal/handlers/oauth/cline.go`) and the Gemini token-refresh writers
(`internal/handlers/chat/gemini_handler.go`) — all pre-existing, none
introduced here. A vault-enabled install still leaks through them until each
is routed through the sealed API in its own change.

**Verification** — a handler-level test drives the real exchange against a
vault-enabled Repo and asserts the tokens are sealed at rest and hydrate back
(`internal/handlers/oauth/authcode_seal_test.go`). It fails on the pre-fix base
with the tokens in the clear:

```
before (pre-fix base):
  access token is plaintext in the data column at rest:
  {"refreshToken":"rt-handler-level-secret","expiresAt":"2026-11-26T07:43:57Z",
   "apiKey":"at-handler-level-secret","accessToken":"at-handler-level-secret"}

after (this branch):
  ok  9router/proxy/internal/handlers/oauth
```