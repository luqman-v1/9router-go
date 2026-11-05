### fix(ci): bump brotli and compress to fix govulncheck (main red on Lint)

- **What:** `github.com/andybalholm/brotli` v1.2.0 → v1.2.7 (GO-2026-6888) and `github.com/klauspost/compress` v1.19.1 → v1.20.2 (GO-2026-6865). Both are indirect via `fhttp`/`tls-client`.
- **Why:** Run 42128288528 on main (935e2b2, headroom autostart #925) failed Lint on govulncheck: both vulns are reachable (`StallReader.Read` via fhttp, jailbreak preset upload). Newer main pushes are still in flight; this clears the gate for all open PRs.
- **How verified:** `govulncheck -show verbose ./...` — "No vulnerabilities found" on symbol results (remaining 3 findings are in import-only packages, not called); `go vet ./...`, cross-compile all 5 targets, `go test ./internal/proxy/...` all ok, `golangci-lint run --new-from-rev` 0 issues.
