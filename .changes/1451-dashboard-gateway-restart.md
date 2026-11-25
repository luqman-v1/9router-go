### Dashboard: restart the gateway from Settings

- Ported from **`giienew/9router-pro@v0.5.109-pro.29`** — `src/app/api/admin/maintenance/route.js`
  (the restart action) and `src/lib/admin/requireAdmin.js` (the operator gate).
  This is **Pro-only behaviour, not upstream parity**: `decolua/9router` has no
  `/api/system/restart`, and Pro's PM2-only wire contract is deliberately not
  ported — the Go binary restarts through its own supervisor, not through PM2.
- `POST /api/system/restart` restarts the gateway, so an operator no longer
  needs a shell. The request must carry `{"confirm":"restart"}` **and** an
  `application/json` content type with no trailing data: the dashboard cookie
  travels on a cross-site form post too, and a request that cannot set a JSON
  body cannot stop the process.
- The mechanism is chosen before anything answers `200`. A supervisor whose
  unit restarts on exit (systemd-user `Restart=always`, launchd `KeepAlive`,
  OpenRC, runit) owns the restart: the process asks to stop and the supervisor
  brings it back. A process nobody supervises (cron, Windows logon entries, a
  plain binary) replaces itself through `updater.RestartSelf`, which re-execs
  the binary only after the listener is closed so the two never race for the
  port. Spawning under a supervisor is the one combination that must not
  happen — it starts a second process beside the one the supervisor starts.
- `updater.RestartSupported()` gates the self-restart path, so a binary this
  process cannot locate answers `500` instead of claiming a restart is under
  way.
- Settings → Gateway Process gains the Restart button: two confirmations, then
  it polls an authenticated endpoint until the replacement answers and reloads.
  In Docker the container exits and returns only under its restart policy.
- Registered in both dashboard route tables (`dashboard.RegisterRoutes` and
  `SetupDashboardRoutes`), with an integration test that boots the real router
  and asserts `400` rather than `404`.
- The route sits on the always-protected list: an engine client API key and an
  anonymous caller on an install with login disabled are both refused, the same
  rule `/api/version/shutdown` follows.
- The supervisor branch requires a supervisor that is **active**: a unit file on
  disk is not a running service, so an inactive or unknown state falls through
  to the self-restart instead of leaving the gateway down.
