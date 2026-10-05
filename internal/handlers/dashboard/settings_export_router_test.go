package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/auth"
)

// The password header is the credential an operator scripting a backup has, and
// the handlers re-verify it themselves with verifyDashboardPassword. That check
// was unreachable over HTTP: /api/settings/database is an always-protected path,
// so RequireAdminAuth refused the request before the handler ever ran — the step-up
// path existed only in tests that mount the handler directly (issue #47).
func TestHandleExportDatabase_PasswordHeaderReachesTheRealRoute(t *testing.T) {
	authTestEnv(t)
	auth.ResetLoginLimiter()

	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)
	storePassword(t, repo, "correct-horse")

	t.Run("correct password header is accepted", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/settings/database", nil)
		req.Header.Set(passwordHeader, "correct-horse")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 with the password header, got %d: %s", rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected the JSON backup, got Content-Type %q", ct)
		}
		if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, ".json") {
			t.Errorf("expected a .json download filename, got Content-Disposition %q", cd)
		}
	})

	t.Run("a wrong password header is refused by the handler", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/settings/database", nil)
		req.Header.Set(passwordHeader, "not-the-password")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		// The middleware only lets the request through; the handler is what
		// actually checks the value, so a wrong one must still fail here.
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 for a wrong password, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("no credential at all is still refused", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/settings/database", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 without any credential, got %d: %s", rec.Code, rec.Body.String())
		}
	})
}