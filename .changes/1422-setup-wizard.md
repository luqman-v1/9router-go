### Added

- First-run setup wizard: a fresh install sets the dashboard password from the
  browser before the first login, instead of typing the well-known `123456`
  default first.
  - `GET /api/setup` reports whether the wizard applies (`status`), the
    database engine (`databaseType`), and whether the request is local
    (`requestIsLocal`).
  - `POST /api/setup` stores the bcrypt password hash and signs the session in.
    It is refused with `403` once a password is stored, when `INITIAL_PASSWORD`
    is set (the operator already chose the password path), from non-loopback
    peers, and on cross-site requests — the same gates
    `mustChangeDefaultPassword` and `POST /api/auth/reset-password` use, so the
    anonymous endpoint is only usable before the gateway is configured. The
    body is read with a 4 KiB bound.
  - The login screen shows the wizard only when no password path exists at
    all: `hasPassword` false, `INITIAL_PASSWORD` unset, and the request from
    the local machine (`requestIsLocal`). With `INITIAL_PASSWORD` set, or from
    a remote browser, the normal login form shows and the wizard never offers
    itself.
  - The wizard asks a second question: how will this gateway be used? Personal
    (one spender: Plans, the token saver and Persona stay hidden and off) or
    Advanced / sharing keys (everything on, as the deployment always was).
    Absent means Advanced, so an older client keeps its behaviour.
  - `advancedMode` is a settings key, editable in Settings → Sidebar: picking a
    preset there moves both the hide list and the mode, and the server refuses
    the gated routes with `403` in personal mode — the sidebar hides the entry
    and a typed URL gets the same answer.

**Verification:** `go test ./internal/handlers/dashboard/` (unit: fresh, remote
refusal, cross-site refusal, short password, second-POST refusal,
INITIAL_PASSWORD gate), `go test -tags=integration -run TestSetupWizard
./internal/integration/` (real router: wizard applies, session cookie issued,
login with the new password, second POST 403, INITIAL_PASSWORD refusal), the
Dashboard E2E suite (fresh install with `INITIAL_PASSWORD` still logs in
through the normal form), `golangci-lint --new-from-rev` 0 issues, `bun run
build` + svelte-check ratchet 0 errors.
