package dashboard

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The reviewer's probe: advancedMode=false with navVisibility.hidden=[] must
// not leave a visible Plans entry that always answers 403. The server side of
// the fix keeps the gate as the authority: the sidebar follows the mode, and
// this test pins that a personal deployment refuses Plans however the hide
// list got there.
func TestAdvancedModeGateWithEmptyHideList(t *testing.T) {
	authTestEnv(t)
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	if err := repo.UpdateSettingsRaw(map[string]any{
		advancedModeSettingKey:  false,
		navVisibilitySettingKey: map[string]any{"hidden": []any{}},
	}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	if advancedModeEnabled(repo) {
		t.Fatalf("advancedModeEnabled = true, want false")
	}

	called := false
	handler := AdvancedModeGate(repo)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/plans", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if called {
		t.Fatalf("personal mode with empty hide list: wrapped handler ran")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("personal mode gate = %d, want 403", rec.Code)
	}
}
