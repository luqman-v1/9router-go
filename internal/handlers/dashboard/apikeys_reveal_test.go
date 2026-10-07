package dashboard

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestApiKeysFullValueOnlyAtCreation pins the F-6 contract for client keys.
//
// The secret is returned exactly once, by create, because the media Run cards
// use it directly as a Bearer credential. Every read path afterwards returns
// only the masked display value — including a fully privileged dashboard
// caller (requireLogin=false), which is precisely the leak this replaced: one
// leaked low-privilege key could previously dump every other key.
func TestApiKeysFullValueOnlyAtCreation(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	createBody := map[string]any{"name": "Reveal Key"}
	bodyBytes, _ := json.Marshal(createBody)
	req := httptest.NewRequest(http.MethodPost, "/api/keys", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create apiKey expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal create response: %v", err)
	}
	full, _ := created["key"].(string)
	if full == "" {
		t.Fatalf("expected the full key once at creation, got %v", created)
	}

	// Even with the dashboard guard fully open, the list must not hand the
	// secret back.
	if err := repo.UpdateSettingsRaw(map[string]any{"requireLogin": false}); err != nil {
		t.Fatalf("disable login: %v", err)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/keys", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %s", rec.Body.String())
	}
	var listed []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("unmarshal keys: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("expected exactly one key, got %v", listed)
	}
	if got, _ := listed[0]["key"].(string); got == full {
		t.Fatal("the list returned the full secret; it must return the masked display value only")
	}
	if got, _ := listed[0]["keyDisplay"].(string); got == "" || got == full {
		t.Fatalf("expected a masked keyDisplay, got %q", got)
	}
	if strings.Contains(rec.Body.String(), full) {
		t.Fatalf("the full secret %q leaked into the list response: %s", full, rec.Body.String())
	}
}