package dashboard

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestApiKeyModelsRouteRoundTrip is the operator side of the resale case: the
// buyer-facing restriction is set through the dashboard route, read back
// through it, and cleared through it. A repository-level contract that no route
// exposes leaves the feature unreachable from the panel, so this asserts the
// shape the panel actually receives.
func TestApiKeyModelsRouteRoundTrip(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	if err := repo.UpdateSettingsRaw(map[string]any{"requireLogin": false}); err != nil {
		t.Fatalf("disable requireLogin: %v", err)
	}
	router := setupTestRouter(repo)

	id, _ := createTestKey(t, router, "Buyer A")

	// A fresh key carries no allowlist, which the gate reads as allow-all.
	if got := keyAllowlist(t, router, id); len(got) != 0 {
		t.Fatalf("fresh key allowlist = %v, want empty (allow-all)", got)
	}

	want := []string{"deepseek/deepseek-chat", "ds/deepseek-v4-pro"}
	if code := putKeyModels(t, router, id, map[string]any{"models": want}); code != http.StatusOK {
		t.Fatalf("PUT /api/keys/{id}/models = %d, want 200", code)
	}
	if got := keyAllowlist(t, router, id); !equalStrings(got, want) {
		t.Errorf("allowlist read back = %v, want %v", got, want)
	}

	// A second PUT replaces rather than appends: an operator narrowing a key
	// must not leave the old, wider list half-applied underneath.
	narrower := []string{"deepseek/deepseek-chat"}
	if code := putKeyModels(t, router, id, map[string]any{"models": narrower}); code != http.StatusOK {
		t.Fatalf("PUT (narrower) = %d, want 200", code)
	}
	if got := keyAllowlist(t, router, id); !equalStrings(got, narrower) {
		t.Errorf("after narrowing = %v, want %v — the second PUT must replace, not append", got, narrower)
	}

	// Clearing restores allow-all, which is the documented meaning of empty.
	if code := putKeyModels(t, router, id, map[string]any{"models": []string{}}); code != http.StatusOK {
		t.Fatalf("PUT (clear) = %d, want 200", code)
	}
	if got := keyAllowlist(t, router, id); len(got) != 0 {
		t.Errorf("after clearing = %v, want empty (allow-all restored)", got)
	}
}

// TestApiKeyModelsRouteRejectsUnknownKey distinguishes "no such key" from "key
// with no allowlist". Both are fatal for an operator acting on the panel: the
// first means the id in the URL is wrong, the second means the policy read
// silently defaulted, and reporting 200 for both hides the difference.
func TestApiKeyModelsRouteRejectsUnknownKey(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	if err := repo.UpdateSettingsRaw(map[string]any{"requireLogin": false}); err != nil {
		t.Fatalf("disable requireLogin: %v", err)
	}
	router := setupTestRouter(repo)

	if code := putKeyModels(t, router, "does-not-exist", map[string]any{"models": []string{"a/b"}}); code != http.StatusNotFound {
		t.Errorf("PUT to an unknown key = %d, want 404 — a 200 here tells the operator the restriction landed when no key was restricted", code)
	}
}

// getKeyModels reads the allowlist the dashboard route reports for id, reusing
// the sibling test's reader so both files exercise the one response shape.
func keyAllowlist(t *testing.T, router http.Handler, id string) []string {
	t.Helper()
	return getKeyModels(t, router, id).Models
}

// putKeyModels writes a body through the dashboard route and returns its status.
func putKeyModels(t *testing.T, router http.Handler, id string, body map[string]any) int {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/keys/"+id+"/models", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec.Code
}

// equalStrings compares two string slices by content and order. The route sorts
// alphabetically per its repository contract, so the assertion is order-sensitive
// on purpose.
func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
