package dashboard

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// createTestKey mints a key through the handler and returns its id and the
// plaintext the create call disclosed.
func createTestKey(t *testing.T, router http.Handler, name string) (id, full string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"name": name})
	req := httptest.NewRequest(http.MethodPost, "/api/keys", bytes.NewReader(body))
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
	id, _ = created["id"].(string)
	full, _ = created["key"].(string)
	if id == "" || full == "" {
		t.Fatalf("create returned id=%q key=%q", id, full)
	}
	return id, full
}

// listKeys reads GET /api/keys and returns the single row, failing when the
// list is not exactly one key — an absent key would make the assertions below
// pass for the wrong reason.
func listKeys(t *testing.T, router http.Handler) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/keys", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list keys: status %d: %s", rec.Code, rec.Body.String())
	}
	var listed []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("unmarshal keys: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("expected exactly one key, got %v", listed)
	}
	return listed[0]
}

// TestApiKeySecretIsReadableAgain is the contract issue #199 asks for: the
// dashboard can reveal and copy a key after it was created. Reversing the F-6
// one-time disclosure is what makes the show/hide and copy buttons in the
// endpoint table do anything.
func TestApiKeySecretIsReadableAgain(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	// requireLogin is on by default, so with no session cookie this caller is
	// anonymous. Disabling it models the default local install — loopback, no
	// login — which is the one setup that must still manage its own keys.
	if err := repo.UpdateSettingsRaw(map[string]any{"requireLogin": false}); err != nil {
		t.Fatalf("disable requireLogin: %v", err)
	}

	_, full := createTestKey(t, router, "Readable Key")

	row := listKeys(t, router)
	if got, _ := row["key"].(string); got != full {
		t.Errorf("key = %q, want the full secret %q", got, full)
	}
	// keyDisplay stays masked: it is the compact identifier the table falls
	// back to for a caller that must not see the secret.
	masked, _ := row["keyDisplay"].(string)
	if masked == "" || masked == full {
		t.Errorf("keyDisplay = %q, want a masked value distinct from the secret", masked)
	}
	if strings.Contains(masked, full) {
		t.Errorf("keyDisplay %q contains the full secret", masked)
	}
}

// TestApiKeySecretHiddenFromApiKeyCallers is the half of #199 that must not
// regress. An engine client key is handed to a client app, so it still gets
// only the masked value — otherwise one leaked key dumps every other key,
// which is exactly what F-6 was merged to prevent.
func TestApiKeySecretHiddenFromApiKeyCallers(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	if err := repo.UpdateSettingsRaw(map[string]any{"requireLogin": true}); err != nil {
		t.Fatalf("set requireLogin: %v", err)
	}
	router := setupTestRouter(repo)

	otherID, otherSecret := createTestKey(t, router, "Other Key")

	req := httptest.NewRequest(http.MethodGet, "/api/keys", nil)
	req.Header.Set("Authorization", "Bearer "+otherSecret)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("api-key caller: status %d: %s", rec.Code, rec.Body.String())
	}

	var listed []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("unmarshal keys: %v", err)
	}
	var found map[string]any
	for _, row := range listed {
		if id, _ := row["id"].(string); id == otherID {
			found = row
			break
		}
	}
	if found == nil {
		t.Fatalf("key %s absent from the list an api-key caller received", otherID)
	}
	if strings.Contains(rec.Body.String(), otherSecret) {
		t.Errorf("GET /api/keys leaked the full secret to an api-key caller: %s", rec.Body.String())
	}
	if got, _ := found["key"].(string); got != "" {
		t.Errorf("key = %q for an api-key caller, want empty", got)
	}
	if got, _ := found["keyDisplay"].(string); got == "" {
		t.Error("keyDisplay is empty for an api-key caller; the table would render nothing")
	}
}

// TestApiKeyFromBeforeHashingIsNotFabricated covers the rows #199 cannot help:
// created under F-6, their plaintext is gone. The list must say so with an
// empty key rather than return the sentinel that now occupies the column,
// which would otherwise render as a bogus token the operator could copy.
func TestApiKeyFromBeforeHashingIsNotFabricated(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	createTestKey(t, router, "Modern Key")
	if err := repo.CreateApiKey("legacy-row", "sk-legacy-secret-value", "Legacy", ""); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	// Convert one row into the F-6 shape, exactly as the old create path did.
	if err := repo.HashApiKeyRow("legacy-row", "verifier", "legacy-lookup", "sk-leg…alue"); err != nil {
		t.Fatalf("HashApiKeyRow: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/keys", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	var listed []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("unmarshal keys: %v", err)
	}
	for _, row := range listed {
		id, _ := row["id"].(string)
		if id != "legacy-row" {
			continue
		}
		if got, _ := row["key"].(string); got != "" {
			t.Errorf("hashed row key = %q, want empty (the plaintext is unrecoverable)", got)
		}
		if got, _ := row["keyDisplay"].(string); got != "sk-leg…alue" {
			t.Errorf("hashed row keyDisplay = %q, want the stored mask", got)
		}
		return
	}
	t.Fatal("legacy row absent from the list")
}