### Added

- First-run setup wizard, inspired by QuantumNous/new-api's `/api/setup`
  (controller/setup.go): a fresh install sets the dashboard password from the
  browser before the first login, instead of typing the well-known `123456`
  default first.
  - `GET /api/setup` reports whether the wizard applies (`status`) and the
    database engine (`databaseType`).
  - `POST /api/setup` stores the bcrypt password hash and signs the session in.
    It is refused with `403` once a password is stored, when `INITIAL_PASSWORD`
    is set (the operator already chose the password path), and from
    non-loopback peers — the same gates `mustChangeDefaultPassword` uses, so
    the anonymous endpoint is only usable before the gateway is configured.
  - The login screen shows the wizard only when no password path exists at
    all: `hasPassword` false and `INITIAL_PASSWORD` unset. With
    `INITIAL_PASSWORD` set, the normal login form shows and the wizard never
    offers itself.

**Verification:** `go test ./internal/handlers/dashboard/` (unit: fresh, remote
refusal, short password, second-POST refusal, INITIAL_PASSWORD gate),
`go test -tags=integration -run TestSetupWizard ./internal/integration/` (real
router: wizard applies, session cookie issued, login with the new password,
second POST 403, INITIAL_PASSWORD refusal), the Dashboard E2E suite (fresh
install with `INITIAL_PASSWORD` still logs in through the normal form),
`golangci-lint --new-from-rev` 0 issues, `bun run build` + svelte-check ratchet
0 errors.
