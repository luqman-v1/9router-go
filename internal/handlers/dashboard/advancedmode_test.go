package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/auth"
)

// advancedModeEnabled defaults to true: a database written before the key
// existed, or one the wizard never touched, keeps every route it always had.
// Only an explicit false turns the surfaces off, so an upgrade cannot lock an
// operator out of Plans they already use.
func TestAdvancedModeDefaultsOn(t *testing.T) {
	authTestEnv(t)
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	if !advancedModeEnabled(repo) {
		t.Fatalf("fresh install: advancedModeEnabled = false, want true (default)")
	}
}

// The wizard's Personal choice writes the key false and fills the sidebar
// hide list, so the first screen after login already matches the mode.
func TestPostSetupPersonalModeWritesKeyAndHideList(t *testing.T) {
	authTestEnv(t)
	t.Setenv("INITIAL_PASSWORD", "")
	auth.ResetLoginLimiter()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	h := NewDashboardHandler(repo)
	req := httptest.NewRequest(http.MethodPost, "/api/setup",
		strings.NewReader(`{"password":"first-run-password","advancedMode":false}`))
	req.RemoteAddr = "127.0.0.1:51000"
	rec := httptest.NewRecorder()
	h.HandlePostSetup(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/setup = %d, want 200; body %s", rec.Code, rec.Body.String())
	}

	raw, err := repo.GetSettingsRaw()
	if err != nil {
		t.Fatalf("GetSettingsRaw: %v", err)
	}
	if v, ok := raw[advancedModeSettingKey].(bool); !ok || v {
		t.Fatalf("advancedMode = %v, want false", raw[advancedModeSettingKey])
	}
	if advancedModeEnabled(repo) {
		t.Fatalf("advancedModeEnabled = true after the wizard wrote false")
	}
	nav, ok := raw[navVisibilitySettingKey].(map[string]any)
	if !ok {
		t.Fatalf("navVisibility missing after Personal setup")
	}
	hidden, _ := nav["hidden"].([]any)
	found := map[string]bool{}
	for _, id := range hidden {
		if s, ok := id.(string); ok {
			found[s] = true
		}
	}
	for _, want := range personalModeHiddenNav {
		if !found[want] {
			t.Errorf("personal hide list missing %q (got %v)", want, hidden)
		}
	}
}

// Advanced keeps the hide list untouched: the operator's existing arrangement
// is not the wizard's business.
func TestPostSetupAdvancedModeLeavesNavAlone(t *testing.T) {
	authTestEnv(t)
	t.Setenv("INITIAL_PASSWORD", "")
	auth.ResetLoginLimiter()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	h := NewDashboardHandler(repo)
	req := httptest.NewRequest(http.MethodPost, "/api/setup",
		strings.NewReader(`{"password":"first-run-password","advancedMode":true}`))
	req.RemoteAddr = "127.0.0.1:51000"
	rec := httptest.NewRecorder()
	h.HandlePostSetup(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/setup = %d, want 200", rec.Code)
	}

	raw, err := repo.GetSettingsRaw()
	if err != nil {
		t.Fatalf("GetSettingsRaw: %v", err)
	}
	if v, ok := raw[advancedModeSettingKey].(bool); !ok || !v {
		t.Fatalf("advancedMode = %v, want true", raw[advancedModeSettingKey])
	}
	if _, present := raw[navVisibilitySettingKey]; present {
		t.Fatalf("navVisibility written by an Advanced setup: %v", raw[navVisibilitySettingKey])
	}
}

// The gate is the half a typed URL cannot walk around: personal mode answers
// 403 on the routes the sidebar hides.
func TestAdvancedModeGateRefusesWhenPersonal(t *testing.T) {
	authTestEnv(t)
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	if err := repo.UpdateSettingsRaw(map[string]any{advancedModeSettingKey: false}); err != nil {
		t.Fatalf("write advancedMode: %v", err)
	}

	called := false
	handler := AdvancedModeGate(repo)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/plans", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if called {
		t.Fatalf("personal mode: the wrapped handler ran")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("personal mode gate = %d, want 403", rec.Code)
	}
}

// And an advanced deployment lets the request through.
func TestAdvancedModeGatePassesWhenAdvanced(t *testing.T) {
	authTestEnv(t)
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	called := false
	handler := AdvancedModeGate(repo)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/plans", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if !called {
		t.Fatalf("advanced mode: the wrapped handler never ran")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("advanced mode gate = %d, want 200", rec.Code)
	}
}

// applyAdvancedModeUpdate drops a non-bool instead of coercing it: a client
// that sends "false" as a string meant something, but guessing which is worse
// than leaving the previous value in place.
func TestApplyAdvancedModeUpdateDropsNonBool(t *testing.T) {
	updates := map[string]any{advancedModeSettingKey: "false"}
	applyAdvancedModeUpdate(updates)
	if _, present := updates[advancedModeSettingKey]; present {
		t.Fatalf("string value coerced into the setting: %v", updates)
	}

	updates = map[string]any{advancedModeSettingKey: false}
	applyAdvancedModeUpdate(updates)
	if v, _ := updates[advancedModeSettingKey].(bool); v {
		t.Fatalf("explicit false lost: %v", updates)
	}
}
