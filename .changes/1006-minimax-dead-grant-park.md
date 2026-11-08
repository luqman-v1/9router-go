### fix(oauth): stop re-spending a parked minimax-code refresh token on chat, usage, and probe paths

A dead `minimax-code-global` grant (`400 invalid_grant: this refresh token can
no longer be used`) was parked correctly, but three paths kept paying for it
anyway:

- The usage route (`GET /api/usage/{conn}`) force-refreshed a
  `reauth_required` account, reproducing the "oauth grant dead" warning and
  bumping the backoff counter behind a request the caller must not make.
- The reactive-401 retry in `tryForwardWithConnection` forced a refresh of an
  already-parked single-use token, re-logging the rejection on the same
  request and then falling through to the guaranteed
  `fallback upstream failed ... 401 invalid access token`.
- Force-fallback (`FORCE_FALLBACK_ON_ALL_UNAVAILABLE`) and the probe bypass
  resurrected parked accounts by earliest cooldown reset, so a terminal grant
  the operator could not repair without re-login kept being selected.

Fixes:

- `internal/handlers/dashboard/usage_credentials.go`: `runUsageRefresh` skips
  a connection that already carries `testStatus=reauth_required` (via the new
  `db.ConnectionRequiresReauth` helper); the panel still renders with the
  stored access token while the badge says to re-authorize.
- `internal/handlers/chat/fallback.go` + `oauth_lock.go`: the reactive-401
  retry checks `connectionParkedForReauth` before forcing a refresh.
- `internal/handlers/chat/force_fallback.go`: `forceMinCooldownConnection`
  never returns a `reauth_required` account — waiting out a dead grant
  cannot help.
- `internal/handlers/chat/connections.go`: the probe bypass and the
  strategy sweep both skip `reauth_required` accounts, so a `/api/models/test`
  or auto-ping against a parked account refuses instead of spending a
  guaranteed 401.
- `internal/proxy/oauth/background.go`: the dead-grant check widens from
  `IsRefreshUnauthorized` (401 only) to `IsRefreshGrantDead`, so the 400
  `invalid_grant` shape minimax-code(-global) and xAI return actually parks
  from the background tick too, with the real status recorded.

Verified: `go vet` + `go test` on `internal/db`, `internal/handlers/chat`,
`internal/handlers/dashboard`, and `internal/proxy/oauth`; new regression
tests `TestConnectionRequiresReauth`, `TestForceMinCooldownConnection_SkipsReauthRequired`,
`TestPingDoesNotBypassParkedDeadGrant`, and
`TestHandleGetConnectionUsage_SkipsRefreshForParkedDeadGrant`.
