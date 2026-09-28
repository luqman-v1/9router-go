package dashboard

import (
	"bytes"
	"database/sql"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/providers"
)

// seedConnection inserts one providerConnections row so the auto free-tier
// combo has a reachable provider to draw models from.
func seedConnection(t *testing.T, db *sql.DB, provider string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, data, createdAt, updatedAt)
		 VALUES ('c1', ?, 'api-key', 'test', '{}', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		provider,
	); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
}

func postJSON(t *testing.T, router interface {
	ServeHTTP(w http.ResponseWriter, r *http.Request)
}, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func putJSON(t *testing.T, router interface {
	ServeHTTP(w http.ResponseWriter, r *http.Request)
}, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestAutoFreeComboCreateAndLock(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	// A bazaarlink connection exists: its registry list has exactly one
	// free-tier model ("auto:free"), which is what the combo must contain.
	const providerID = "bazaarlink"
	seedConnection(t, repo.RawDB(), providerID)

	rec := postJSON(t, router, "/api/combos/auto-free", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("auto-free expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	models, _ := created["models"].([]any)
	if len(models) != 1 {
		t.Fatalf("expected 1 free model, got %v", models)
	}
	if got := models[0]; got != "bzl/auto:free" {
		t.Errorf("expected bzl/auto:free, got %v", got)
	}

	// Delete is refused: the combo is locked.
	req := httptest.NewRequest(http.MethodDelete, "/api/combos/"+AutoFreeComboID, nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("delete locked combo expected 403, got %d: %s", rec.Code, rec.Body.String())
	}

	// Reordering the same model set is allowed.
	rec = putJSON(t, router, "/api/combos/"+AutoFreeComboID, map[string]any{
		"name":   "Auto Free Tier",
		"kind":   "auto-free",
		"models": []string{"bzl/auto:free"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("reorder expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// A strategy-only update carries no models field — that is how the card's
	// strategy dropdown persists — and must not be read as "models changed".
	rec = putJSON(t, router, "/api/combos/"+AutoFreeComboID, map[string]any{
		"strategy": "round-robin",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("strategy-only update expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Changing the model set is refused.
	rec = putJSON(t, router, "/api/combos/"+AutoFreeComboID, map[string]any{
		"models": []string{"bzl/claude-opus-4.7"},
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("model-set change expected 403, got %d: %s", rec.Code, rec.Body.String())
	}

	// Renaming is refused.
	rec = putJSON(t, router, "/api/combos/"+AutoFreeComboID, map[string]any{
		"name":  "Renamed",
		"kind":  "auto-free",
		"models": []string{"bzl/auto:free"},
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("rename expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAutoFreeComboEmptyWithoutConnections(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	rec := postJSON(t, router, "/api/combos/auto-free", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("no free models expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestIsFreeTierModel(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  bool
	}{
		{"colon free suffix", "deepseek-v4.1-flash:free", true},
		{"slash free suffix", "kilo-auto/free", true},
		{"dash free suffix", "mimo-v2.5-free", true},
		{"paid model", "claude-opus-4.7", false},
		{"free inside name only", "free-tier-proxy", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := providers.IsFreeTierModel(tt.model); got != tt.want {
				t.Errorf("IsFreeTierModel(%q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}
