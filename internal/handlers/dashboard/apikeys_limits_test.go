package dashboard

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCreateApiKeyAcceptsTheCeilings proves a key can be bounded at the moment
// it is created: the operator sets a budget and a context ceiling in the same
// form as the policy dialog writes them.
//
// A key created unbounded and bounded afterwards is the same key, but it is
// two steps and a window in which the key spends without a ceiling.
func TestCreateApiKeyAcceptsTheCeilings(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	if err := repo.UpdateSettingsRaw(map[string]any{"requireLogin": false}); err != nil {
		t.Fatalf("disable requireLogin: %v", err)
	}
	router := setupTestRouter(repo)

	body, _ := json.Marshal(map[string]any{
		"name":         "Bounded Key",
		"tokenQuota":   10_000_000,
		"contextLimit": 200_000,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/keys", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	row := listKeys(t, router)
	if got := intFromJSON(row["tokenQuota"]); got != 10_000_000 {
		t.Errorf("tokenQuota = %d, want 10000000", got)
	}
	if got := intFromJSON(row["contextLimit"]); got != 200_000 {
		t.Errorf("contextLimit = %d, want 200000", got)
	}
}

// TestCreateApiKeyWithoutCeilingsStaysUnbounded proves the new fields are
// optional: a key created without them is unbounded, exactly as before.
func TestCreateApiKeyWithoutCeilingsStaysUnbounded(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	if err := repo.UpdateSettingsRaw(map[string]any{"requireLogin": false}); err != nil {
		t.Fatalf("disable requireLogin: %v", err)
	}
	router := setupTestRouter(repo)

	createTestKey(t, router, "Plain Key")

	row := listKeys(t, router)
	if got := intFromJSON(row["tokenQuota"]); got != 0 {
		t.Errorf("tokenQuota = %d, want 0 (unlimited)", got)
	}
	if got := intFromJSON(row["contextLimit"]); got != 0 {
		t.Errorf("contextLimit = %d, want 0 (no ceiling)", got)
	}
}

// TestCreateApiKeyRejectsANegativeCeiling proves a refused limit leaves no key
// behind: a negative quota would read as "unlimited" in the limiter while
// looking like a configured limit in the dashboard.
func TestCreateApiKeyRejectsANegativeCeiling(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	if err := repo.UpdateSettingsRaw(map[string]any{"requireLogin": false}); err != nil {
		t.Fatalf("disable requireLogin: %v", err)
	}
	router := setupTestRouter(repo)

	for _, payload := range []map[string]any{
		{"name": "Bad Quota", "tokenQuota": -1},
		{"name": "Bad Context", "contextLimit": -5},
	} {
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/api/keys", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("payload %v: status = %d, want 400", payload, rec.Code)
		}
	}

	// The refused creates must not have left a key behind.
	req := httptest.NewRequest(http.MethodGet, "/api/keys", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	var listed []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("unmarshal keys: %v", err)
	}
	if len(listed) != 0 {
		t.Errorf("a refused create left %d keys behind, want 0", len(listed))
	}
}

// TestUpdateApiKeyPolicySetsTheContextLimit proves the policy dialog can set
// the context ceiling, and that a rename leaves it alone.
func TestUpdateApiKeyPolicySetsTheContextLimit(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	if err := repo.UpdateSettingsRaw(map[string]any{"requireLogin": false}); err != nil {
		t.Fatalf("disable requireLogin: %v", err)
	}
	router := setupTestRouter(repo)
	id, _ := createTestKey(t, router, "Context Key")

	body, _ := json.Marshal(map[string]any{"contextLimit": 128_000})
	req := httptest.NewRequest(http.MethodPut, "/api/keys/"+id, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	row := listKeys(t, router)
	if got := intFromJSON(row["contextLimit"]); got != 128_000 {
		t.Errorf("contextLimit = %d, want 128000", got)
	}

	// A partial update that only renames must not wipe the ceiling.
	rename, _ := json.Marshal(map[string]any{"name": "Renamed"})
	req = httptest.NewRequest(http.MethodPut, "/api/keys/"+id, bytes.NewReader(rename))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("rename = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	row = listKeys(t, router)
	if got := intFromJSON(row["contextLimit"]); got != 128_000 {
		t.Errorf("contextLimit after rename = %d, want 128000 (must be preserved)", got)
	}
}

// TestUpdateApiKeyPolicyRejectsANegativeContextLimit proves a negative ceiling
// is refused rather than stored.
func TestUpdateApiKeyPolicyRejectsANegativeContextLimit(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	if err := repo.UpdateSettingsRaw(map[string]any{"requireLogin": false}); err != nil {
		t.Fatalf("disable requireLogin: %v", err)
	}
	router := setupTestRouter(repo)
	id, _ := createTestKey(t, router, "Negative Context Key")

	body, _ := json.Marshal(map[string]any{"contextLimit": -1})
	req := httptest.NewRequest(http.MethodPut, "/api/keys/"+id, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}
