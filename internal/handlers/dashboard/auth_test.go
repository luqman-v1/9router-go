package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	json "encoding/json/v2"

	"golang.org/x/crypto/bcrypt"

	"9router/proxy/internal/auth"
	"9router/proxy/internal/db"
)

// authTestEnv pins the JWT secret and data dir so session cookies are
// deterministic and tests never touch the real ~/.9router.
func authTestEnv(t *testing.T) {
	t.Helper()
	t.Setenv("JWT_SECRET", "dashboard-auth-test-secret")
	t.Setenv("DATA_DIR", t.TempDir())
}

func storePassword(t *testing.T, repo *db.Repo, password string) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	if err := repo.UpdateSettingsRaw(map[string]any{"password": string(hash)}); err != nil {
		t.Fatalf("store password: %v", err)
	}
}

func postLogin(t *testing.T, h *DashboardHandler, password string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"password":` + mustJSON(t, password) + `}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.HandleAuthLogin(rec, req)
	return rec
}

func mustJSON(t *testing.T, v string) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func TestHandleAuthLogin_InvalidPassword(t *testing.T) {
	authTestEnv(t)
	auth.ResetLoginLimiter()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	rec := postLogin(t, NewDashboardHandler(repo), "wrong")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Invalid password") {
		t.Errorf("expected Invalid password message, got %s", rec.Body.String())
	}
}
func TestHandleAuthLogin_SetsSessionCookie(t *testing.T) {
	authTestEnv(t)
	auth.ResetLoginLimiter()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	storePassword(t, repo, "s3cret-pass")
	rec := postLogin(t, NewDashboardHandler(repo), "s3cret-pass")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	var session *http.Cookie
	for _, c := range cookies {
		if c.Name == auth.CookieName {
			session = c
		}
	}
	if session == nil {
		t.Fatalf("expected an %s cookie, got %v", auth.CookieName, cookies)
	}
	if !session.HttpOnly {
		t.Error("session cookie must be httpOnly")
	}
	if !auth.Verify(session.Value, auth.Secret()) {
		t.Error("session cookie must carry a verifiable token")
	}
}

func TestHandleAuthStatus_SessionRoundTrip(t *testing.T) {
	authTestEnv(t)
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	storePassword(t, repo, "s3cret-pass")
	h := NewDashboardHandler(repo)

	// Anonymous: requireLogin defaults on, and no session.
	rec := httptest.NewRecorder()
	h.HandleAuthStatus(rec, httptest.NewRequest(http.MethodGet, "/api/auth/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var status map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status["requireLogin"] != true {
		t.Errorf("expected requireLogin true when unset, got %v", status["requireLogin"])
	}
	if status["authenticated"] != false {
		t.Errorf("expected authenticated false, got %v", status["authenticated"])
	}
	if status["hasPassword"] != true {
		t.Errorf("expected hasPassword true, got %v", status["hasPassword"])
	}

	// With the login cookie, the same endpoint reports authenticated.
	login := postLogin(t, h, "s3cret-pass")
	req := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	for _, c := range login.Result().Cookies() {
		req.AddCookie(c)
	}
	rec = httptest.NewRecorder()
	h.HandleAuthStatus(rec, req)
	_ = json.Unmarshal(rec.Body.Bytes(), &status)
	if status["authenticated"] != true {
		t.Errorf("expected authenticated true with cookie, got %v", status["authenticated"])
	}
}

func TestHandleRequireLogin_ReflectsSetting(t *testing.T) {
	authTestEnv(t)
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	h := NewDashboardHandler(repo)

	read := func() map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		h.HandleRequireLogin(rec, httptest.NewRequest(http.MethodGet, "/api/settings/require-login", nil))
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out
	}

	if got := read()["requireLogin"]; got != true {
		t.Errorf("unset requireLogin should be true, got %v", got)
	}
	if err := repo.UpdateSettingsRaw(map[string]any{"requireLogin": false}); err != nil {
		t.Fatalf("update settings: %v", err)
	}
	if got := read()["requireLogin"]; got != false {
		t.Errorf("requireLogin false should be honored, got %v", got)
	}
}

func TestHandleAuthLogout_ClearsCookie(t *testing.T) {
	authTestEnv(t)
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	NewDashboardHandler(repo).HandleAuthLogout(rec, httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	found := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.CookieName {
			found = true
			if c.Value != "" {
				t.Errorf("logout cookie should be empty, got %q", c.Value)
			}
			if c.MaxAge >= 0 {
				t.Errorf("logout cookie should expire immediately, got MaxAge %d", c.MaxAge)
			}
		}
	}
	if !found {
		t.Fatal("logout must clear the session cookie")
	}
}

func TestHandleAuthLogin_LocksOutAfterFiveFails(t *testing.T) {
	authTestEnv(t)
	auth.ResetLoginLimiter()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	h := NewDashboardHandler(repo)

	var last *httptest.ResponseRecorder
	for range 5 {
		last = postLogin(t, h, "wrong")
	}
	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 on the 5th failure, got %d: %s", last.Code, last.Body.String())
	}
	if last.Header().Get("Retry-After") == "" {
		t.Error("lockout response must carry a Retry-After header")
	}
	var out map[string]any
	if err := json.Unmarshal(last.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode lockout: %v", err)
	}
	if _, ok := out["retryAfter"]; !ok {
		t.Error("lockout body must include retryAfter")
	}
	if _, ok := out["resetHint"]; !ok {
		t.Error("lockout body must include resetHint")
	}
	if rec := postLogin(t, h, "wrong"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("locked bucket must stay 429, got %d", rec.Code)
	}
}

func TestHandleAuthLogin_RemoteDefaultPasswordMustChange(t *testing.T) {
	authTestEnv(t)
	auth.ResetLoginLimiter()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	h := NewDashboardHandler(repo)

	body := `{"password":"123456"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
	req.RemoteAddr = "203.0.113.10:1234"
	req.Host = "203.0.113.10:20130"
	rec := httptest.NewRecorder()
	h.HandleAuthLogin(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for remote default password, got %d: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["mustChangePassword"] != true {
		t.Errorf("expected mustChangePassword=true, got %v", out["mustChangePassword"])
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.CookieName && c.Value != "" {
			t.Error("remote default login must not issue a session cookie")
		}
	}
}

func TestHandleAuthLogin_RemoteDefaultPassword_HostSpoofingFails(t *testing.T) {
	authTestEnv(t)
	auth.ResetLoginLimiter()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	h := NewDashboardHandler(repo)

	// Attacker sends Host: localhost from a remote IP
	body := `{"password":"123456"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
	req.RemoteAddr = "203.0.113.10:1234"
	req.Host = "localhost"
	rec := httptest.NewRecorder()
	h.HandleAuthLogin(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for remote default password even with Host: localhost, got %d: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["mustChangePassword"] != true {
		t.Errorf("expected mustChangePassword=true, got %v", out["mustChangePassword"])
	}
}

func TestHandleAuthLogin_RemoteDefaultPasswordRotation(t *testing.T) {
	authTestEnv(t)
	auth.ResetLoginLimiter()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	h := NewDashboardHandler(repo)

	// Remote request supplying newPassword alongside default password rotates and succeeds
	body := `{"password":"123456","newPassword":"custom-new-password"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
	req.RemoteAddr = "203.0.113.10:1234"
	req.Host = "203.0.113.10:20130"
	rec := httptest.NewRecorder()
	h.HandleAuthLogin(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for password rotation, got %d: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["success"] != true || out["mustChangePassword"] != false {
		t.Errorf("expected success=true and mustChangePassword=false, got %v", out)
	}
	hasSessionCookie := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.CookieName && c.Value != "" {
			hasSessionCookie = true
		}
	}
	if !hasSessionCookie {
		t.Error("successful rotation must issue a session cookie")
	}

	// Verify that the new password is now stored and works
	if !h.verifyDashboardPassword("custom-new-password") {
		t.Error("new password must verify successfully")
	}
	if h.verifyDashboardPassword("123456") {
		t.Error("old default password must no longer verify")
	}
}

func TestHandleAuthLogin_TunnelAndSSOGates(t *testing.T) {
	authTestEnv(t)
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	storePassword(t, repo, "s3cret-pass")
	h := NewDashboardHandler(repo)

	if err := repo.UpdateSettingsRaw(map[string]any{
		"tunnelUrl": "https://tunnel.example.com",
	}); err != nil {
		t.Fatalf("seed tunnel: %v", err)
	}
	tunnelReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"s3cret-pass"}`))
	tunnelReq.Host = "tunnel.example.com"
	tunnelRec := httptest.NewRecorder()
	auth.ResetLoginLimiter()
	h.HandleAuthLogin(tunnelRec, tunnelReq)
	if tunnelRec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for tunnel login, got %d: %s", tunnelRec.Code, tunnelRec.Body.String())
	}

	if err := repo.UpdateSettingsRaw(map[string]any{
		"tunnelDashboardAccess": true,
		"authMode":              "sso",
		"ssoType":               "oidc",
		"oidcIssuerUrl":         "https://idp.example.com",
		"oidcClientId":          "client-id",
		"oidcClientSecret":      "client-secret",
	}); err != nil {
		t.Fatalf("seed sso: %v", err)
	}
	auth.ResetLoginLimiter()
	if rec := postLogin(t, h, "s3cret-pass"); rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for SSO-only password login, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAuthStatus_SSOFlagsAndIdentity(t *testing.T) {
	authTestEnv(t)
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	h := NewDashboardHandler(repo)

	anon := httptest.NewRecorder()
	h.HandleAuthStatus(anon, httptest.NewRequest(http.MethodGet, "/api/auth/status", nil))
	var status map[string]any
	if err := json.Unmarshal(anon.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status["oidcConfigured"] != false || status["samlConfigured"] != false {
		t.Errorf("expected both SSO flags false, got %v", status)
	}
	if status["oidcName"] != nil || status["samlName"] != nil {
		t.Errorf("anonymous status must null the SSO identity, got %v", status)
	}

	if err := repo.UpdateSettingsRaw(map[string]any{
		"oidcIssuerUrl":    "https://idp.example.com/",
		"oidcClientId":     "client-id",
		"oidcClientSecret": "client-secret",
		"samlEntryPoint":   "https://idp.example.com/sso",
		"samlCert":         "cert-body",
	}); err != nil {
		t.Fatalf("seed sso settings: %v", err)
	}
	flagged := httptest.NewRecorder()
	h.HandleAuthStatus(flagged, httptest.NewRequest(http.MethodGet, "/api/auth/status", nil))
	status = map[string]any{}
	if err := json.Unmarshal(flagged.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status["oidcConfigured"] != true || status["samlConfigured"] != true {
		t.Errorf("expected both SSO flags true, got %v", status)
	}
}
