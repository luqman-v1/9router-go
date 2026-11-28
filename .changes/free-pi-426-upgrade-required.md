### fix(free-pi): send the published official CLI version instead of an unpublished 0.7

Requests to `fp/deepseek/deepseek-v4-flash` failed with HTTP 426
`{"code":"upgrade_required","message":"This free-pi-cli version is not an official release..."}`:
the gateway sent `x-client-version: 0.7`, but the server's live
`GET /client-version` publishes `min=latest=0.6.28` and npm has never released
a `0.7`. The 426 handler treated this as the older `client_outdated` gate: it
refreshed the snapshot, saw no floor advance past `0.7`, skipped the retry, and
relayed 426 to the client. The `0.7` compiled-in floor was introduced by #1075
when live `latest` was still below it; it became the bug once the server began
enforcing that only published versions are accepted.

- **Publish `latest` is the ceiling.** `ResolveFreePiClientVersion` now clamps
  the resolved header to the server's published `latest` when one is available;
  a stored or compiled-in version above it can no longer reach the wire.
  `min` remains the floor, so a server-side raise before npm publishes still
  wins. The compiled-in fallback drops to `0.6.28` (the verified current
  release) and is only used when `/client-version` is unreachable.
- **Code-aware 426 handling.** `freePiVersionRetry` reads the upstream error
  code: `upgrade_required` ("not an official release") forces the header to the
  published `latest` and retries once even when no version floor advanced;
  `client_outdated` keeps the existing refresh-and-retry path. The retry stays
  at exactly one attempt, with no re-login and no loop.
- **Contracts updated with sources.** Two older assertions that a stored/compiled
  version could exceed the server's published `latest` were replaced: live feed
  2026-11-28, npm registry `free-pi-cli@0.6.28`, and the official CLI's
  `src/version.ts` (`CLI_VERSION` from `package.json`) disprove them. The fixture
  now records `min=latest=0.6.28`.

## Live e2e (no mocks)

Built the patched binary and ran it against a copy of the real `DATA_DIR` on an
isolated port, using the operator's real free-pi connection
(`fp-b7be88ef107b`, stored login pin `0.6.16`) and real API key. No stub, fake
upstream, or synthetic response was involved. Three consecutive
`POST /v1/chat/completions` requests for `fp/deepseek/deepseek-v4-flash` went
through the gateway to `api.freepi.ai` and returned **HTTP 200** with real
completions (`PONG`, `OK`, `OK`); the gateway logged
`[usage] logged provider=free-pi model=deepseek/deepseek-v4-flash ... conn=fp-b7be88ef107b egress=direct`.
No 426 or `upgrade_required` appeared in the log. The live feed still reports
`min=latest=0.6.28`, confirming the pinned `0.6.16` was lifted per request
without re-login. The original database was never used by the test process.

Verified: a reproduction stub (426 `upgrade_required`, `latest=0.6.28`) failed
on the base with one attempt and the 426 relayed; after the fix both attempts
carry `0.6.28` and the retry succeeds. `go test` for `internal/providers` and
`internal/proxy/executor`, `go vet`, `gofmt`, and `make test-integration` pass.
