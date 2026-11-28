package dashboard

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// importHandlerKey is a secret that passes the import shape check: `sk-` plus
// 32 characters of the key alphabet.
const importHandlerKey = "sk-handlerimport-0123456789abcdef"

func postImport(t *testing.T, router http.Handler, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/keys/import", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// TestHandleImportApiKeyCreatesThenReplays is the endpoint contract: the first
// call is a 201, the replay of the same body is a 200, and neither response
// carries a hash column.
func TestHandleImportApiKeyCreatesThenReplays(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	id := "550e8400-e29b-41d4-a716-446655440000"
	body := map[string]any{"id": id, "key": importHandlerKey, "name": "Imported"}
	first := postImport(t, router, body)
	if first.Code != http.StatusCreated {
		t.Fatalf("first import = %d, want 201 (body: %s)", first.Code, first.Body.String())
	}
	created := decodeImport(t, first)
	if created["key"] == nil || !strings.Contains(created["key"].(string), "•") {
		t.Errorf("created key = %v, want a masked value", created["key"])
	}
	assertNoHashColumns(t, created)

	second := postImport(t, router, body)
	if second.Code != http.StatusOK {
		t.Fatalf("replay = %d, want 200 (body: %s)", second.Code, second.Body.String())
	}
	replayed := decodeImport(t, second)
	if replayed["created"] != false {
		t.Errorf("replay created = %v, want false", replayed["created"])
	}
	if replayed["id"] != created["id"] {
		t.Errorf("replay id = %v, want the original %v", replayed["id"], created["id"])
	}
	assertNoHashColumns(t, replayed)

	// The row is stored hashed, and the plaintext is not recoverable from it.
	row := listKeys(t, router)
	if got, _ := row["key"].(string); got != "" {
		t.Errorf("stored secret = %q, want it withheld from the list", got)
	}
}

// TestHandleImportApiKeyRefusesBadRequests pins the 400/409 split: a malformed
// body is the caller's error, a body that would overwrite a stored secret is a
// conflict.
func TestHandleImportApiKeyRefusesBadRequests(t *testing.T) {
	tests := []struct {
		name     string
		body     map[string]any
		wantCode int
		wantText string
	}{
		{
			name:     "missing key",
			body:     map[string]any{"name": "No secret"},
			wantCode: http.StatusBadRequest,
			wantText: "key is required",
		},
		{
			name:     "key too short",
			body:     map[string]any{"key": "sk-short"},
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "key with a trailing newline",
			body:     map[string]any{"key": importHandlerKey + "\n"},
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "id is not a UUID",
			body:     map[string]any{"id": "not-a-uuid", "key": importHandlerKey},
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "name is too long",
			body:     map[string]any{"key": importHandlerKey, "name": strings.Repeat("a", maxAPIKeyNameLen+1)},
			wantCode: http.StatusBadRequest,
			wantText: "name is too long",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, cleanup := setupTestDB(t)
			defer cleanup()
			router := setupTestRouter(repo)

			rec := postImport(t, router, tt.body)
			if rec.Code != tt.wantCode {
				t.Fatalf("POST /api/keys/import = %d, want %d (body: %s)", rec.Code, tt.wantCode, rec.Body.String())
			}
			if tt.wantText != "" && !strings.Contains(rec.Body.String(), tt.wantText) {
				t.Errorf("refusal body = %q, want it to contain %q", rec.Body.String(), tt.wantText)
			}
			// A refused import leaves no row behind.
			if keys, err := repo.GetApiKeys(); err != nil {
				t.Fatalf("GetApiKeys: %v", err)
			} else if len(keys) != 0 {
				t.Errorf("refused import left %d row(s) behind", len(keys))
			}
		})
	}
}

// TestHandleImportApiKeyConflictKeepsStoredSecret is the 409 path: the id is
// taken by a row holding a different secret, so the import must refuse rather
// than overwrite.
func TestHandleImportApiKeyConflictKeepsStoredSecret(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	seedID := "550e8400-e29b-41d4-a716-446655440001"
	if rec := postImport(t, router, map[string]any{"id": seedID, "key": importHandlerKey}); rec.Code != http.StatusCreated {
		t.Fatalf("seed import = %d, want 201 (body: %s)", rec.Code, rec.Body.String())
	}

	rec := postImport(t, router, map[string]any{
		"id":  seedID,
		"key": "sk-handlerimport-0123456789zzzzzz",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("conflicting import = %d, want 409 (body: %s)", rec.Code, rec.Body.String())
	}

	row := listKeys(t, router)
	if got, _ := row["id"].(string); got != seedID {
		t.Errorf("stored id = %q, want %q", got, seedID)
	}
}

// TestHandleImportApiKeyDuplicateSecretNoIDIsConflict pins the OmniRoute
// parity rule (Issue #1557): a duplicate secret without a matching id is
// always a conflict, never a silent 200 redirect to the existing row.
func TestHandleImportApiKeyDuplicateSecretNoIDIsConflict(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	seedID := "550e8400-e29b-41d4-a716-446655440002"
	if rec := postImport(t, router, map[string]any{"id": seedID, "key": importHandlerKey}); rec.Code != http.StatusCreated {
		t.Fatalf("seed import = %d, want 201 (body: %s)", rec.Code, rec.Body.String())
	}

	rec := postImport(t, router, map[string]any{"key": importHandlerKey})
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate secret without id = %d, want 409 (body: %s)", rec.Code, rec.Body.String())
	}

	keys, err := repo.GetApiKeys()
	if err != nil {
		t.Fatalf("GetApiKeys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("conflict left %d rows, want 1", len(keys))
	}
}

func decodeImport(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal import response %q: %v", rec.Body.String(), err)
	}
	return out
}

// assertNoHashColumns is the disclosure guard: the verifier and the lookup
// index are not values a client has any use for, and neither may appear in an
// import response.
func assertNoHashColumns(t *testing.T, body map[string]any) {
	t.Helper()
	for _, k := range []string{"keyHash", "lookupHash"} {
		if _, ok := body[k]; ok {
			t.Errorf("import response carries %q", k)
		}
	}
}
