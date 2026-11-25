package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/auth"
)

// authCookieValue pulls the session cookie a response set, or "" when it set
// none.
func authCookieValue(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.CookieName && c.Value != "" {
			return c.Value
		}
	}
	return ""
}

func TestPasswordChangeRevokesOlderSessions(t *testing.T) {
	authTestEnv(t)
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	auth.SetSessionEpoch(0, "")
	t.Cleanup(func() { auth.SetSessionEpoch(0, "") })

	h := NewDashboardHandler(repo)
	storePassword(t, repo, "first-password")

	// The token under test is one the login route actually minted, not a
	// hand-built one: the point is that a real session dies with the change.
	login := postLogin(t, h, "first-password")
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d: %s", login.Code, login.Body.String())
	}
	oldToken := authCookieValue(t, login)
	if oldToken == "" {
		t.Fatal("login set no session cookie")
	}
	if !auth.Verify(oldToken, auth.Secret()) {
		t.Fatal("the login cookie must verify before the change")
	}

	body := `{"currentPassword":"first-password","newPassword":"second-password"}`
	rec := httptest.NewRecorder()
	h.HandleUpdateSettings(rec, httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("settings status = %d: %s", rec.Code, rec.Body.String())
	}

	raw, err := repo.GetSettingsRaw()
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if got := auth.SessionTokenVersion(raw); got != 1 {
		t.Errorf("tokenVersion = %d, want 1", got)
	}
	if auth.Verify(oldToken, auth.Secret()) {
		t.Error("a session issued before the password change must stop verifying")
	}

	fresh := authCookieValue(t, rec)
	if fresh == "" {
		t.Fatal("the password change must re-issue the caller's session cookie")
	}
	if !auth.Verify(fresh, auth.Secret()) {
		t.Error("the re-issued cookie must verify")
	}
}

func TestPasswordChangeBumpsEpochPerChange(t *testing.T) {
	authTestEnv(t)
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	auth.SetSessionEpoch(0, "")
	t.Cleanup(func() { auth.SetSessionEpoch(0, "") })

	h := NewDashboardHandler(repo)
	storePassword(t, repo, "pw-one")

	change := func(current, next string) {
		t.Helper()
		raw, err := repo.GetSettingsRaw()
		if err != nil {
			t.Fatalf("read settings: %v", err)
		}
		before := auth.SessionTokenVersion(raw)
		if err := h.changeDashboardPassword(current, next); err != nil {
			t.Fatalf("changeDashboardPassword: %v", err)
		}
		raw, err = repo.GetSettingsRaw()
		if err != nil {
			t.Fatalf("read settings: %v", err)
		}
		if got := auth.SessionTokenVersion(raw); got != before+1 {
			t.Errorf("tokenVersion = %d, want %d", got, before+1)
		}
	}

	change("pw-one", "pw-two")
	change("pw-two", "pw-three")
}
