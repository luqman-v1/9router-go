package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/auth"
)

// The dashboard is behind a session cookie, but the backup download demanded the
// re-typed password header as well. A browser link cannot set a custom header, so
// the download was refused with 401 no matter how the user reached it (issue #47).
// A logged-in session has to be enough on its own.
func TestHandleExportDatabase_DashboardSessionAuthorizes(t *testing.T) {
	authTestEnv(t)
	auth.ResetLoginLimiter()

	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	h := NewDashboardHandler(repo)
	storePassword(t, repo, "correct-horse")

	login := postLogin(t, h, "correct-horse")
	if login.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", login.Code, login.Body.String())
	}
	session := login.Result().Cookies()

	req := httptest.NewRequest(http.MethodGet, "/api/settings/database?format=zip", nil)
	for _, c := range session {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.HandleExportDatabase(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("a logged-in session must be able to download the backup, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "9router-backup.json") {
		t.Error("the archive must carry the backup payload")
	}
}

// Loosening the session path must not open the export to an anonymous caller on
// an instance that does require a password.
func TestHandleExportDatabase_AnonymousStillRefusedWhenPasswordRequired(t *testing.T) {
	authTestEnv(t)
	auth.ResetLoginLimiter()

	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	h := NewDashboardHandler(repo)
	storePassword(t, repo, "correct-horse")

	req := httptest.NewRequest(http.MethodGet, "/api/settings/database", nil)
	rec := httptest.NewRecorder()
	h.HandleExportDatabase(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for an anonymous caller, got %d: %s", rec.Code, rec.Body.String())
	}
}

// The password header keeps working: it is the step-up path for a script that
// already holds the credential and carries no session.
func TestHandleExportDatabase_PasswordHeaderStillAuthorizes(t *testing.T) {
	authTestEnv(t)
	auth.ResetLoginLimiter()

	repo, cleanup := setupSettingsTestDB(t)
	defer cleanup()
	h := NewDashboardHandler(repo)
	storePassword(t, repo, "correct-horse")

	req := httptest.NewRequest(http.MethodGet, "/api/settings/database", nil)
	req.Header.Set(passwordHeader, "correct-horse")
	rec := httptest.NewRecorder()
	h.HandleExportDatabase(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for the password header, got %d: %s", rec.Code, rec.Body.String())
	}
}
