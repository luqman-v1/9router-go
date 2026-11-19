### Added

- `agy-acp` now has a real Google login in the dashboard, matching the `antigravity` add-account flow: "Connect Google Account" button → risk notice → Google consent popup → automatic callback exchange. Previously the provider fell through to the generic OAuth modal that only offered manual token paste, so adding an account never opened a Google login link.
  - New endpoints `GET /api/oauth/agy-acp/authorize` and `POST /api/oauth/agy-acp/exchange` share the Google consumer OAuth client, scopes, and callback machinery with antigravity (refactored into `writeGoogleAuthorize` / `exchangeGoogleCode`), but store their **own** `agy-acp` connection rows (`agcpy-<hash>` ids) — a login never writes or mutates an antigravity row (§3 provider isolation). Credential sharing between the providers remains the explicit per-connection `sourceConnection` reference.
  - `agy-acp` registered in `KnownOAuthConfigs` (standard Google refresher), `accessTokenOnlyProviders` (apiKey mirrors the access token), and `sensitiveProviders` (background refresh pacing), so its copied tokens refresh proactively and on reactive 401 like antigravity's.

### Fixed

- Every turn on a dashboard-logged-in `agy-acp` connection failed with HTTP 502 "connection carries no accessToken/refreshToken and no sourceConnection": the request path marshals `ConnectionData`, which has no `refreshToken` field, so copy mode never saw the stored token pair. `resolveACPCredentials` now falls back to the connection's stored DB row (`ownACPCredentials`), which also picks up tokens a background refresh has rotated. Regression test reproduces the exact request-path map.

### Verification

- `go test ./internal/handlers/oauth/ ./internal/proxy/oauth/ ./internal/providers/` pass, including new table-driven handler tests (authorize URL shape, exchange stores an agy-acp-only row, re-login upserts, error cases).
- Live UI proof against a running gateway (isolated DB copy): Providers → Antigravity CLI (ACP) → Connect Google Account → risk notice → modal shows the `accounts.google.com` consent URL with the correct client id and `redirect_uri=http://localhost:20132/callback`; pasting a callback URL posts to `/api/oauth/agy-acp/exchange` (dummy code correctly surfaces Google's 400 as "token exchange returned status 400" instead of "Direct OAuth login is not supported").
- `bun test src scripts` — 845 pass; `bun run ratchet:svelte` — 0 unresolved identifiers, baseline 74 unchanged; `bun run build` — dist 4.8 MB.
- `golangci-lint run --new-from-rev=origin/main` on touched packages — 0 issues.
