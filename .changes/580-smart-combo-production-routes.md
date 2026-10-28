### Fix: the smart-combo routes were missing from the router the binary builds

The smart-rules editor answered `404 page not found` on every preview and every **Reconcile rules** click, while the rest of the feature worked.

The two routes were registered only in `dashboard.RegisterRoutes` — the table the dashboard package's own tests mount. The binary builds its dashboard API from `SetupDashboardRoutes` in `internal/handlers/router.go`, which is a separate registration, so `POST /api/combos/{id}/smart/preview` and `POST /api/combos/reconcile` were reachable in a unit test and 404 on the real server. The combo create/update that carries `matchRules` and the `GET` that returns the derived `autoMembers` exist in both tables, which is why the bug read as "the rules section is broken" rather than "the feature is missing".

This is the same failure the node-limit pair had in #432, and the comment that fix left 20 lines above the combos block documents the trap; both routes are now mounted in both places, with the reason recorded at the registration site.

- `TestSmartComboRulesAreReachableThroughTheRouter` drives both routes through the router the binary builds and reads the membership back through `GET /api/combos`. Verified to fail without the router change (`POST smart preview = 404, want 200 (body: 404 page not found)`) and to pass with it.
- Audited every route in `dashboard.RegisterRoutes` against `SetupDashboardRoutes`. One further pre-existing gap is **not** touched here: `GET/POST /api/combos/presets` is also registered in the test table only, so combo presets are a 404 on the real server too. Worth its own issue.

Verified with `go build ./...`, `go test ./internal/...` (only the pre-existing Windows-local `browserlogin` failure, identical on a pristine `main` checkout), `go test -tags=integration ./internal/integration/...` and `golangci-lint run --new-from-rev=38d0c27 ./...` (0 issues).
