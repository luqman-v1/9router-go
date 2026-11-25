//go:build integration

package integration

import (
	"net/http"
	"testing"

	"9router/proxy/internal/auth"
)

// The real server mounts its dashboard routes in SetupDashboardRoutes, a table
// separate from the one the package tests mount, so a route registered in only
// one of them 404s in production. The endpoint refuses a request without its
// confirmation word, so a 400 proves the handler is reachable — the CLI token
// is the operator credential the always-protected gate admits.
func TestDashboardRestartRouteIsMounted(t *testing.T) {
	env := newEnv(t)

	res := env.Post(t, "/api/system/restart", map[string]any{},
		WithoutAPIKey(),
		WithHeader(auth.CLITokenHeader, auth.CLIToken()),
	)
	if res.Status == http.StatusNotFound {
		t.Fatal("POST /api/system/restart = 404: the route is missing from SetupDashboardRoutes")
	}
	if res.Status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d — the route must refuse a request without its confirmation word", res.Status, http.StatusBadRequest)
	}
}

// Restarting the process is a control action on the box. An engine client API
// key is a credential handed to a tenant, so it must never reach this route —
// the same rule /api/version/shutdown already follows. The harness's default
// request carries that key, which is exactly the caller this test keeps out.
func TestDashboardRestartRejectsEngineAPIKey(t *testing.T) {
	env := newEnv(t)

	res := env.Post(t, "/api/system/restart", map[string]any{"confirm": "restart"})
	if res.Status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d — an engine client API key must not be able to restart the gateway", res.Status, http.StatusUnauthorized)
	}
}

// An install with login disabled still must not expose a process restart to
// anyone who can reach the port.
func TestDashboardRestartRejectsAnonymousCaller(t *testing.T) {
	env := newEnv(t)

	res := env.Post(t, "/api/system/restart", map[string]any{"confirm": "restart"}, WithoutAPIKey())
	if res.Status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d — the restart route requires an operator credential", res.Status, http.StatusUnauthorized)
	}
}
