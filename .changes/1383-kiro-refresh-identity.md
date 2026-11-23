### fix(kiro): social token refresh now sends the full Kiro IDE runtime identity

The social refresh endpoint was still receiving the hardcoded `User-Agent: kiro-cli/1.0.0`, so its identity did not match the one the inference path had already been updated to send. The three social-refresh call sites upstream were changed to build the identity from the refresh token; the Go refresher had not followed.

- **One shared identity helper.** `internal/kiroidentity` is a new leaf package both the inference proxy and the OAuth refresher import, so neither has to copy the other's UA format and no import cycle is introduced. It emits the exact upstream `User-Agent` and `X-Amz-User-Agent` strings for both client kinds — `codewhispererstreaming` with `m/E` for inference, `codewhispererruntime` with `m/N,E` for refresh — and bumps `KiroIDEVersion` from `1.4.121` to `1.4.154`.
- **Machine-id seed priority.** The helper derives the sha256-hex machine id from the first non-empty of `clientId`, `refreshToken`, `profileArn`, `apiKey`, `accessToken`, falling back to `kiro-anonymous`. The inference path feeds it `clientId`/`profileArn` then the bearer token, preserving its existing behavior; the refresh path feeds it the refresh token, so the id stays stable as access tokens rotate.
- **Both headers on the wire.** `refreshKiroSocial` now sets `User-Agent` and `X-Amz-User-Agent` from the runtime identity. The dead `kiroSocialUA` variable is gone.
- **Verification.** New table-driven tests pin the exact UA strings for both client kinds, seed priority and cross-refresh stability, and an `http.RoundTripper` test captures the real outgoing refresh request — rewriting the hardcoded `https://prod.us-east-1.auth.desktop.kiro.dev/refreshToken` URL to an `httptest` server — to prove both headers, the runtime surface and the refresh-token seed are actually sent.

Upstream: decolua/9router#1da78e2bf. Closes #1383.
