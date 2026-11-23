### feat(kiro): align stream identity with upstream and port the CreateResponse fallback

Kiro's client-version gate and the `create_response_api` experiment both live on
the upstream side, so a Go gateway that still announced IDE `1.4.121` or a stale
`kiro-cli/1.0.0` refresh UA could fail before a request ever reached the model,
and a 429 carrying `USER_REQUEST_RATE_EXCEEDED` had no in-request recovery.

- **Shared identity.** `internal/proxy/kiroidentity` holds the upstream
  `1.4.154` IDE / `1.0.0` SDK constants and builds the exact streaming and
  runtime User-Agent pair. Inference calls, AWS OIDC refresh and social refresh
  now use it, so no caller can fall below the version gate on its own. The
  machine id is a SHA-256 of the seeded identifier, never a raw token.
- **CreateResponse fallback.** Ported upstream's `/v1/responses` path. `auto`
  retries a legacy `429 USER_REQUEST_RATE_EXCEEDED` against the region-validated
  runtime endpoint; `always` skips the legacy call; `off` keeps today's behavior.
  Non-OK CreateResponse answers are passed through with their own status and
  body so 401/403 refresh and normal error classification still apply. The
  response stream is translated incrementally into OpenAI SSE chunks, including
  reasoning, tool calls, usage and non-stream folding.
- **Unchanged by design:** 429s on the same credential are still not retried,
  the three endpoint surfaces and account cooldown are untouched, and no
  generic 429 triggers the fallback. `external_idp` refresh and refresh-token
  seeding on inference remain separate follow-ups.

- **Dependency security.** Bumped `github.com/gorilla/websocket` from `v1.5.3`
  to the fixed pseudo-version `v1.5.4-0.20240701034025-d67f41855da4` so the
  reachable `GO-2026-6278` finding no longer fails the CI `govulncheck` gate.
  The same finding was reproducible on clean `origin/main`; no file in this PR
  introduced it.

Verified: `go vet ./...`, `go test ./... -count=1`, the binary build,
`golangci-lint --new-from-rev=origin/main` (0 issues) and `govulncheck`
(No vulnerabilities found) all pass;
Kiro tests cover exact UA strings, both refresh paths, the three fallback modes,
body translation, region validation, backoff and stream folding. No `web/`
changes.
