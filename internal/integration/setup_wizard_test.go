//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"

	"9router/proxy/internal/auth"
)

// anonymous by necessity. These tests pin the full HTTP path through the real
// router: a fresh install sets a password and lands with a session, a
// configured install refuses the POST, and GET reports the wizard's state.

func TestSetupWizardFreshInstall(t *testing.T) {
	auth.ResetLoginLimiter()
	env := newEnv(t)

	// Fresh install: the wizard applies.
	res := env.Get(t, "/api/setup", WithoutAPIKey())
	if res.Status != http.StatusOK {
		t.Fatalf("GET /api/setup = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	var status struct {
		Status       bool   `json:"status"`
		DatabaseType string `json:"databaseType"`
	}
	res.Decode(t, &status)
	if !status.Status {
		t.Fatalf("fresh install: GET /api/setup status = false, want true")
	}
	if status.DatabaseType != "sqlite" {
		t.Errorf("databaseType = %q, want sqlite", status.DatabaseType)
	}

	// Setting the password signs the session in.
	post := env.Post(t, "/api/setup", map[string]string{"password": "first-run-password"}, WithoutAPIKey())
	if post.Status != http.StatusOK {
		t.Fatalf("POST /api/setup = %d, want 200 (body: %s)", post.Status, truncate(post.Body))
	}
	var set struct {
		Success bool `json:"success"`
	}
	post.Decode(t, &set)
	if !set.Success {
		t.Fatalf("POST /api/setup success = false")
	}
	if len(post.Header.Values("Set-Cookie")) == 0 {
		t.Errorf("POST /api/setup set no session cookie")
	}

	// The login path now accepts the password that was set.
	login := env.Post(t, "/api/auth/login", map[string]string{"password": "first-run-password"}, WithoutAPIKey())
	if login.Status != http.StatusOK {
		t.Fatalf("POST /api/auth/login = %d, want 200 (body: %s)", login.Status, truncate(login.Body))
	}

	// A second setup attempt is refused: the endpoint is not a password reset.
	again := env.Post(t, "/api/setup", map[string]string{"password": "second-attempt"}, WithoutAPIKey())
	if again.Status != http.StatusForbidden {
		t.Fatalf("second POST /api/setup = %d, want 403", again.Status)
	}

	// And the wizard no longer applies.
	res2 := env.Get(t, "/api/setup", WithoutAPIKey())
	var status2 struct {
		Status bool `json:"status"`
	}
	res2.Decode(t, &status2)
	if status2.Status {
		t.Fatalf("configured install: GET /api/setup status = true, want false")
	}
}

func TestSetupWizardRejectsShortPassword(t *testing.T) {
	auth.ResetLoginLimiter()
	env := newEnv(t)

	res := env.Post(t, "/api/setup", map[string]string{"password": "short"}, WithoutAPIKey())
	if res.Status != http.StatusBadRequest {
		t.Fatalf("POST /api/setup (short) = %d, want 400", res.Status)
	}
}

// INITIAL_PASSWORD is a configured password path: the wizard must not apply
// and the POST must be refused, otherwise a browser-typed password would
// override the operator's chosen first-login password.
func TestSetupWizardRefusedWhenInitialPasswordSet(t *testing.T) {
	auth.ResetLoginLimiter()
	t.Setenv("INITIAL_PASSWORD", "123456")
	env := newEnv(t)

	res := env.Post(t, "/api/setup", map[string]string{"password": "first-run-password"}, WithoutAPIKey())
	if res.Status != http.StatusForbidden {
		t.Fatalf("POST /api/setup (INITIAL_PASSWORD set) = %d, want 403", res.Status)
	}

	status := env.Get(t, "/api/setup", WithoutAPIKey())
	var body struct {
		Status bool `json:"status"`
	}
	status.Decode(t, &body)
	if body.Status {
		t.Fatalf("GET /api/setup (INITIAL_PASSWORD set) status = true, want false")
	}
}

// sessionCookie turns the Set-Cookie of a completed wizard into the Cookie
// header the dashboard guards read. The setup POST signs the operator in, so
// the mode gate is reachable exactly the way a real operator reaches it.
func sessionCookie(t *testing.T, res *Result) string {
	t.Helper()
	for _, cookie := range res.Header.Values("Set-Cookie") {
		name, value, ok := strings.Cut(cookie, "=")
		if !ok {
			continue
		}
		if strings.TrimSpace(name) == "auth_token" {
			if v, _, ok := strings.Cut(value, ";"); ok {
				return "auth_token=" + v
			}
			return "auth_token=" + value
		}
	}
	t.Fatalf("no auth_token cookie in the setup response")
	return ""
}

// The wizard's second question is a sidebar preset, not an authz switch: a
// personal deployment hides the operator surfaces, and the routes stay
// reachable. Plans only arranges the operator's own keys on a gateway that is
// already behind a password, so making it 403 bought nothing and cost a second
// source of truth for the sidebar.
func TestSetupWizardPersonalModeHidesSurfaces(t *testing.T) {
	auth.ResetLoginLimiter()
	env := newEnv(t)

	res := env.Post(t, "/api/setup", map[string]any{
		"password":     "first-run-password",
		"advancedMode": false,
	}, WithoutAPIKey())
	if res.Status != http.StatusOK {
		t.Fatalf("POST /api/setup (personal) = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	cookie := sessionCookie(t, res)

	// The sidebar hide list was written, so the first screen matches the mode.
	status := env.Get(t, "/api/settings", WithoutAPIKey(), WithHeader("Cookie", cookie))
	var settings struct {
		NavVisibility map[string]any `json:"navVisibility"`
	}
	status.Decode(t, &settings)
	hidden, _ := settings.NavVisibility["hidden"].([]any)
	found := map[string]bool{}
	for _, id := range hidden {
		if id, ok := id.(string); ok {
			found[id] = true
		}
	}
	for _, want := range []string{"plans", "token-saver", "jailbreak"} {
		if !found[want] {
			t.Errorf("personal hide list missing %q (got %v)", want, hidden)
		}
	}

	// And the route still answers: hidden, not forbidden.
	plans := env.Get(t, "/api/plans", WithoutAPIKey(), WithHeader("Cookie", cookie))
	if plans.Status != http.StatusOK {
		t.Fatalf("GET /api/plans (personal) = %d, want 200 — the mode is a preset, not an authz gate", plans.Status)
	}
}

// Advanced writes no hide list: the deployment keeps every entry it had.
func TestSetupWizardAdvancedModeLeavesNavAlone(t *testing.T) {
	auth.ResetLoginLimiter()
	env := newEnv(t)

	res := env.Post(t, "/api/setup", map[string]any{
		"password":     "first-run-password",
		"advancedMode": true,
	}, WithoutAPIKey())
	if res.Status != http.StatusOK {
		t.Fatalf("POST /api/setup (advanced) = %d, want 200", res.Status)
	}
	cookie := sessionCookie(t, res)

	status := env.Get(t, "/api/settings", WithoutAPIKey(), WithHeader("Cookie", cookie))
	var settings struct {
		NavVisibility any `json:"navVisibility"`
	}
	status.Decode(t, &settings)
	if settings.NavVisibility != nil {
		t.Fatalf("advanced setup wrote a hide list: %v", settings.NavVisibility)
	}
}

// Skip keeps the default password and turns the login requirement off: the
// operator asked for a dashboard with no password, which is the same switch
// Settings > Security exposes as "Require login".
func TestSetupWizardSkipPasswordDisablesLogin(t *testing.T) {
	auth.ResetLoginLimiter()
	env := newEnv(t)

	res := env.Post(t, "/api/setup", map[string]any{"skipPassword": true}, WithoutAPIKey())
	if res.Status != http.StatusOK {
		t.Fatalf("POST /api/setup (skip) = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	var body struct {
		Success         bool `json:"success"`
		PasswordSkipped bool `json:"passwordSkipped"`
	}
	res.Decode(t, &body)
	if !body.Success || !body.PasswordSkipped {
		t.Fatalf("skip response = %+v, want success + passwordSkipped", body)
	}

	// No hash was stored, and the dashboard stopped asking for a login.
	settingsRes := env.Get(t, "/api/settings", WithoutAPIKey())
	var settings struct {
		RequireLogin *bool  `json:"requireLogin"`
		Password     string `json:"password"`
	}
	settingsRes.Decode(t, &settings)
	if settings.RequireLogin == nil || *settings.RequireLogin {
		t.Fatalf("requireLogin = %v after skip, want false", settings.RequireLogin)
	}
	if settings.Password != "" {
		t.Fatalf("skip stored a password hash")
	}

	// The dashboard answers without a session.
	status := env.Get(t, "/api/auth/status", WithoutAPIKey())
	var authStatus struct {
		Authenticated bool `json:"authenticated"`
		RequireLogin  bool `json:"requireLogin"`
	}
	status.Decode(t, &authStatus)
	if authStatus.RequireLogin {
		t.Fatalf("auth status still reports requireLogin true after skip")
	}
}
