### Fix(zed): close all HTTP response bodies, fix Lint findings from PR #536

PR #536 (`feat(zed)`) introduced 11 Lint findings that reddened the `Lint`
job for every branch and PR based on `main`:

- `bodyclose` (2): `ZedHTTPWithRetry`, response body left open on the timeout and
  replay paths (`internal/proxy/executor/zed.go`)
- `errcheck` (6): unchecked `resp.Body.Close()` (×3), `hw.Close()`, `bodyCloser.Close()`,
  `ResponseBuf.Write`
- `gofmt` (1): `internal/handlers/dashboard/connection_probe_zed_test.go`
- `staticcheck` ST1005 (2): capitalized error strings in
  `internal/handlers/dashboard/zed_catalog.go`

Fixes: every `defer resp.Body.Close()` wrapped in the repo's checked form
(`defer func() { _ = resp.Body.Close() }()`), `bodyCloser.Close()` and
`ResponseBuf.Write` checked, error strings lowercased, `gofmt` on the test.

Verified: `golangci-lint run --new-from-rev=origin/main ./...` → 0 issues,
`go build ./cmd/9router-go/` OK, Zed tests pass on
`internal/providers`, `internal/proxy/executor` and
`internal/handlers/dashboard`.
