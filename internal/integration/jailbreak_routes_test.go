//go:build integration

package integration

import (
	"net/http"
	"testing"
)

// TestJailbreakRoutesAreMounted proves every jailbreak dashboard endpoint is
// reachable on the router the real binary mounts (app.ProvideRouter), not only
// on the tests-only dashboard.RegisterRoutes table. A route registered in only
// one of the two tables passes the package unit tests and 404s on the live
// server, which is how this pair was nearly shipped.
func TestJailbreakRoutesAreMounted(t *testing.T) {
	env := newEnv(t)

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/jailbreak"},
		{http.MethodPut, "/api/jailbreak"},
		{http.MethodGet, "/api/jailbreak/presets"},
		{http.MethodPut, "/api/jailbreak/presets"},
		{http.MethodGet, "/api/jailbreak/models"},
		{http.MethodPut, "/api/jailbreak/models"},
		{http.MethodGet, "/api/jailbreak/excludes"},
		{http.MethodPut, "/api/jailbreak/excludes"},
		{http.MethodPost, "/api/jailbreak/import"},
		{http.MethodPost, "/api/jailbreak/presets/upload"},
	}

	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			res := env.Do(t, tc.method, tc.path, map[string]any{})
			// 404 is the failure this test exists to catch: the route is not
			// mounted. Anything else (200, 400, 405) means it is.
			if res.Status == http.StatusNotFound {
				t.Fatalf("%s %s = 404 on the production router; the route is not mounted (body: %s)",
					tc.method, tc.path, truncate(res.Body))
			}
			t.Logf("%s %s -> %d", tc.method, tc.path, res.Status)
		})
	}
}
