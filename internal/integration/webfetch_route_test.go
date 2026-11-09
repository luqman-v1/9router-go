//go:build integration

package integration

import (
	"net/http"
	"testing"
)

// HandleWebFetch has existed since the media lane landed, but the route was
// never registered, so /v1/web/fetch answered 404 no matter what the caller
// sent. A handler that no request can reach is indistinguishable from a missing
// feature, and the unit tests in internal/handlers/media call it directly so
// they could not see the gap. This test goes through the production router for
// that reason: it fails with 404 on the pre-fix code and with 400 — the
// handler's own validation — afterwards.
//
// The assertion is deliberately the *validation* status rather than 200. A 200
// would need a live provider; a 400 proves the request reached HandleWebFetch,
// which is the only thing this route change is supposed to prove.
func TestWebFetchRouteIsRegistered(t *testing.T) {
	env := newEnv(t)

	// Empty body: HandleWebFetch reads the body, fails to find a url, and
	// answers 400 "missing or invalid url".
	res := env.Post(t, "/v1/web/fetch", map[string]any{})
	if res.Status == http.StatusNotFound {
		t.Fatalf("POST /v1/web/fetch = 404, route is not registered (body: %s)", truncate(res.Body))
	}
	if res.Status != http.StatusBadRequest {
		t.Fatalf("POST /v1/web/fetch = %d, want 400 from the handler's own validation (body: %s)",
			res.Status, truncate(res.Body))
	}
}
