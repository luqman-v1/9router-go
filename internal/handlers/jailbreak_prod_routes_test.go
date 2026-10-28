package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/auth"
	"9router/proxy/internal/db"

	"github.com/go-chi/chi/v5"
)

// TestSetupServerRouter_JailbreakRoutesAreLive pins every jailbreak endpoint to
// the router the server actually mounts (SetupServerRouter), not the tests-only
// dashboard.RegisterRoutes table.
//
// The excludes and import routes were registered in both tables, yet the live
// gateway answered 404 for them while the dashboard package tests passed —
// the same failure shape as the codex reset-credit pair documented above. This
// test is the production-side guard: it boots SetupServerRouter directly and
// asserts that no jailbreak path falls through to chi's not-found handler.
func TestSetupServerRouter_JailbreakRoutesAreLive(t *testing.T) {
	t.Setenv("JWT_SECRET", "jailbreak-router-test-secret")
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	r := chi.NewRouter()
	SetupServerRouter(r, repo, nil)

	token, err := auth.Sign("jailbreak-router-test-secret", time.Now())
	if err != nil {
		t.Fatalf("sign session token: %v", err)
	}
	authed := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	for _, tc := range []struct{ method, path string }{
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
	} {
		rec := authed(tc.method, tc.path)
		if rec.Code == http.StatusNotFound {
			t.Errorf("%s %s is not registered on the production router: %s",
				tc.method, tc.path, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "404 page not found") {
			t.Errorf("%s %s fell through to chi's not-found handler", tc.method, tc.path)
		}
	}
}
