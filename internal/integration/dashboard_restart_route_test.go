//go:build integration

package integration

import (
	"net/http"
	"testing"
)

// The real server mounts its dashboard routes in SetupDashboardRoutes, a table
// separate from the one the package tests mount, so a route registered in only
// one of them 404s in production. This endpoint refuses a request without its
// confirmation word, which makes it safe to probe: a 400 proves it is mounted.
func TestDashboardRestartRouteIsMounted(t *testing.T) {
	env := newEnv(t)

	res := env.Post(t, "/api/system/restart", map[string]any{})
	if res.Status == http.StatusNotFound {
		t.Fatal("POST /api/system/restart = 404: the route is missing from SetupDashboardRoutes")
	}
	if res.Status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d — the route must refuse a request without its confirmation word", res.Status, http.StatusBadRequest)
	}
}
