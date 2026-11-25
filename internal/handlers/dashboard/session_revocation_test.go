package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// The epoch is the one setting a client must never be able to write: doing so
// resurrects every token a password change revoked.
func TestTokenVersionIsNotWritable(t *testing.T) {
	authTestEnv(t)
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	auth.SetSessionEpoch(0, "")
	t.Cleanup(func() { auth.SetSessionEpoch(0, "") })

	h := NewDashboardHandler(repo)
	storePassword(t, repo, "pw-one")
	if err := h.changeDashboardPassword("pw-one", "pw-two"); err != nil {
		t.Fatalf("setup change: %v", err)
	}

	cases := []struct{ name, body string }{
		{"direct write", `{"tokenVersion":0}`},
		{"write beside a password change", `{"currentPassword":"pw-two","newPassword":"pw-three","tokenVersion":0}`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := repo.GetSettingsRaw()
			if err != nil {
				t.Fatalf("read settings: %v", err)
			}
			before := auth.SessionTokenVersion(raw)
			beforeHash, _ := raw["password"].(string)

			rec := httptest.NewRecorder()
			h.HandleUpdateSettings(rec, httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(tt.body)))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}

			raw, err = repo.GetSettingsRaw()
			if err != nil {
				t.Fatalf("read settings: %v", err)
			}
			if got := auth.SessionTokenVersion(raw); got != before {
				t.Errorf("tokenVersion = %d, want unchanged (%d)", got, before)
			}
			// The rejection must land before the password write, or a failed
			// request would still have revoked every session.
			if after, _ := raw["password"].(string); after != beforeHash {
				t.Error("a rejected mixed payload must not change the password")
			}
		})
	}
}

// Changing a password must not turn an SSO operator into a password user.
func TestPasswordChangeKeepsSSOClaims(t *testing.T) {
	authTestEnv(t)
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	auth.SetSessionEpoch(0, "")
	t.Cleanup(func() { auth.SetSessionEpoch(0, "") })

	h := NewDashboardHandler(repo)
	storePassword(t, repo, "pw-one")

	sso, err := auth.SignWith(auth.Secret(), time.Now(), auth.SessionClaims{
		Oidc:      true,
		OidcName:  "Alice",
		OidcEmail: "alice@example.com",
	})
	if err != nil {
		t.Fatalf("sign sso token: %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/settings",
		strings.NewReader(`{"currentPassword":"pw-one","newPassword":"pw-two"}`))
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: sso})
	rec := httptest.NewRecorder()
	h.HandleUpdateSettings(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	fresh := authCookieValue(t, rec)
	if fresh == "" {
		t.Fatal("the change must re-issue the cookie")
	}
	// Decode the re-issued token through the same claim reader the dashboard
	// uses, so the assertion covers what /api/auth/status would render.
	check := httptest.NewRequest(http.MethodGet, "/", nil)
	check.AddCookie(&http.Cookie{Name: auth.CookieName, Value: fresh})
	claims := auth.SessionClaimSet(check)
	if claims == nil {
		t.Fatal("the re-issued cookie must verify")
	}
	if !claims.Oidc || claims.OidcEmail != "alice@example.com" {
		t.Errorf("claims = %+v, want the OIDC identity preserved", claims)
	}
	if claims.TV != 1 {
		t.Errorf("tv = %d, want the new epoch", claims.TV)
	}
}

// A session captured before a password RESET keeps working until the process
// restarts if only the stored hash changes: `ph` is checked against an
// in-memory fingerprint, and the reset handler is the one password write that
// does not go through changeDashboardPassword. The reset must reinstall the
// epoch in-process.
func TestPasswordResetRevokesOlderSessionsInProcess(t *testing.T) {
	authTestEnv(t)
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	auth.SetSessionEpoch(0, "")
	t.Cleanup(func() { auth.SetSessionEpoch(0, "") })
	ResetResetPasswordLimiter()

	h := NewDashboardHandler(repo)
	storePassword(t, repo, "before-reset")
	// Boot installs the epoch from the stored hash; without this the login
	// mints a token at (0, "") and a reset back to "" would still match.
	auth.LoadSessionEpoch(repo)

	login := postLogin(t, h, "before-reset")
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d: %s", login.Code, login.Body.String())
	}
	oldToken := authCookieValue(t, login)
	if oldToken == "" {
		t.Fatal("login set no session cookie")
	}
	if !auth.Verify(oldToken, auth.Secret()) {
		t.Fatal("the login cookie must verify before the reset")
	}

	req := httptest.NewRequest(http.MethodPost, "/api/auth/reset-password", strings.NewReader(`{"confirm":true}`))
	req.RemoteAddr = "127.0.0.1:41234"
	req.Host = "localhost:20128"
	rec := httptest.NewRecorder()
	h.HandleResetPassword(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset status = %d: %s", rec.Code, rec.Body.String())
	}

	// No restart: the same process, the token minted before the reset.
	if auth.Verify(oldToken, auth.Secret()) {
		t.Error("the pre-reset token still verifies after the reset, in-process")
	}

	// And the gateway is not bricked: the fallback password is accepted again.
	// Checked through the verifier rather than the login route, which
	// deliberately refuses the default password to a remote caller.
	if !h.verifyDashboardPassword(defaultInitialPassword) {
		t.Error("the default password must verify after a reset")
	}
}

// The setup wizard writes the first stored hash. A session minted on a fresh
// install (no password yet) must not survive it: its `ph` is the fingerprint of
// the empty hash, and the wizard replaces that hash. Without the in-process
// refresh the token keeps verifying until a restart.
func TestSetupRevokesOlderSessionsInProcess(t *testing.T) {
	authTestEnv(t)
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	auth.SetSessionEpoch(0, "")
	t.Cleanup(func() { auth.SetSessionEpoch(0, "") })

	h := NewDashboardHandler(repo)
	// Boot on a fresh install: epoch installed from the (empty) stored state,
	// so a token minted here carries PH = sha256("").
	auth.LoadSessionEpoch(repo)

	stale, err := auth.Sign(auth.Secret(), time.Now())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if !auth.Verify(stale, auth.Secret()) {
		t.Fatal("the pre-setup token must verify before the wizard runs")
	}

	body := `{"password":"first-password"}`
	req := httptest.NewRequest(http.MethodPost, "/api/setup", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:41234"
	req.Host = "localhost:20128"
	rec := httptest.NewRecorder()
	h.HandlePostSetup(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup status = %d: %s", rec.Code, rec.Body.String())
	}

	// No restart: same process, the token minted before the wizard.
	if auth.Verify(stale, auth.Secret()) {
		t.Error("a token minted on the passwordless install still verifies after setup")
	}
	fresh := authCookieValue(t, rec)
	if fresh == "" {
		t.Fatal("setup must hand back a session")
	}
	if !auth.Verify(fresh, auth.Secret()) {
		t.Error("the session setup minted does not verify against the state it wrote")
	}
}
