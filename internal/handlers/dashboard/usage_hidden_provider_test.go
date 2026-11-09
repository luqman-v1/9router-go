package dashboard

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleGetUsageProviders_ExcludesHidden(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	h := NewDashboardHandler(repo)

	// Seed connections: two visible, one canonical hidden (mimo-free), one alias hidden (mmf)
	for _, s := range []struct{ id, provider, authType string }{
		{"c-openai", "openai", "apikey"},
		{"c-claude", "claude", "oauth"},
		{"c-mimo", "mimo-free", "apikey"},
		{"c-mmf", "mmf", "apikey"},
	} {
		if err := repo.CreateProviderConnection(s.id, s.provider, s.authType, s.id, "sk-"+s.provider); err != nil {
			t.Fatalf("seed %s: %v", s.provider, err)
		}
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/usage/providers", nil)

	h.HandleGetUsageProviders(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Providers []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	for _, p := range resp.Providers {
		if p.ID == "mimo-free" || p.ID == "mmf" {
			t.Errorf("hidden provider %s should be excluded from /api/usage/providers", p.ID)
		}
	}

	if len(resp.Providers) != 2 {
		t.Errorf("expected 2 visible providers, got %d (%+v)", len(resp.Providers), resp.Providers)
	}
}
