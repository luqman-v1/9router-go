### fix(oauth): route connection writes through the sealed vault API

OAuth handlers wrote `providerConnections.data` through `RawDB().Exec`, so with
`ROUTER_MASTER_KEY` set access and refresh tokens landed in `data` in the clear
(breaking the vault's "never plaintext at rest" guarantee), and the paired
`SELECT data` read-back returned ciphertext that `json.Unmarshal` silently
rejected — dropping the refresh token on relogin/rotation and logging the user
out.

`authcode`, `pkce`, `device`, `antigravity` (Google) and the Gemini
`projectId` stamp now write through `ReplaceProviderConnectionPayload` /
`CreateProviderConnectionFull`, which seal credentials before the write and
hydrate them on read. A vault-less install is unchanged (data passes through
untouched, no extra statement).
