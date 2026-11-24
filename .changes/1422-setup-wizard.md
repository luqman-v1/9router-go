### Added

- First-run setup wizard: a fresh install sets the dashboard password from the
  browser before the first login, instead of typing the well-known `123456`
  default first.
  - `GET /api/setup` reports whether the wizard applies (`status`), the
    database engine (`databaseType`), and whether the request is local
    (`requestIsLocal`).
  - `POST /api/setup` stores the bcrypt password hash and signs the session in.
    It is refused with `403` once setup is complete (a stored hash, a
    configured `INITIAL_PASSWORD`, or an explicit "no password here"), from
    non-loopback peers, and on cross-site requests — the same gates
    `mustChangeDefaultPassword` and `POST /api/auth/reset-password` use, so the
    anonymous endpoint is only usable before the gateway is configured. The
    body is read with a 4 KiB bound.
  - **Skip** keeps the default password and turns the login requirement off in
    one step (`requireLogin: false`) — the same switch Settings > Security
    exposes, so a gateway the operator left open is not shown a setup form that
    would turn it back on.
  - The wizard asks a second question: how will this gateway be used? Personal
    writes the sidebar hide list (Plans, token saver, Persona); Advanced writes
    nothing. The routes stay reachable either way — this is a sidebar preset,
    not an authz switch: Plans only arranges the operator's own keys on a
    gateway that is already behind a password.
  - The login screen shows the wizard only when setup is incomplete, no
    `INITIAL_PASSWORD` is configured, the login requirement is on, and the
    request comes from the local machine.
  - Every password field in the wizard and in Settings > Security has a
    show/hide control, so a long generated password can be read back before it
    is committed. Settings > Security labels the login switch by what it does
    ("Require a password") and says what turning it off means.

**Verification:** `go test ./internal/handlers/dashboard/` (unit: fresh set,
skip, remote refusal, cross-site refusal, short password, second-POST refusal,
`INITIAL_PASSWORD` gate), `go test -tags=integration -run TestSetupWizard
./internal/integration/` (real router: wizard applies, session cookie issued,
login with the new password, personal writes the hide list while `/api/plans`
still answers 200, skip disables the login requirement, second POST 403), the
Dashboard E2E suite, `golangci-lint --new-from-rev` 0 issues, `bun run build` +
svelte-check ratchet 0 errors. Browser-verified: skip lands in the dashboard,
an anonymous visit reaches it, and the three Settings password fields toggle
between masked and plain.
