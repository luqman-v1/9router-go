//go:build integration

package integration

import (
	"net/http"
	"testing"

	"9router/proxy/internal/auth"
)

// The first-run setup endpoints (inspired by new-api's /api/setup) are
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
