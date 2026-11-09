package dashboard

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestHandleExportProviderKeys(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	handler := NewDashboardHandler(repo)

	p1 := 1
	p2 := 2
	if err := repo.CreateProviderConnectionFull("conn-1", "anthropic", "apikey", "Key 1", &p1, `{"apiKey":"sk-ant-12345"}`); err != nil {
		t.Fatalf("failed to seed conn-1: %v", err)
	}
	if err := repo.CreateProviderConnectionFull("conn-2", "anthropic", "oauth", "OAuth 1", &p2, `{"accessToken":"tok-ant-67890"}`); err != nil {
		t.Fatalf("failed to seed conn-2: %v", err)
	}
	if err := repo.CreateProviderConnectionFull("conn-3", "openai", "apikey", "OpenAI Key", &p1, `{"apiKey":"sk-oai-99999"}`); err != nil {
		t.Fatalf("failed to seed conn-3: %v", err)
	}

	t.Run("exports provider keys accurately", func(t *testing.T) {
		r := chi.NewRouter()
		r.Get("/api/providers/{id}/export-keys", handler.HandleExportProviderKeys)

		req := httptest.NewRequest(http.MethodGet, "/api/providers/anthropic/export-keys", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}

		var res struct {
			Connections []struct {
				ID       string `json:"id"`
				Name     string `json:"name"`
				AuthType string `json:"authType"`
				Key      string `json:"key"`
			} `json:"connections"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		if len(res.Connections) != 2 {
			t.Fatalf("expected 2 connections for anthropic, got %d", len(res.Connections))
		}

		keys := map[string]string{}
		for _, c := range res.Connections {
			keys[c.ID] = c.Key
		}

		if keys["conn-1"] != "sk-ant-12345" {
			t.Errorf("expected conn-1 key 'sk-ant-12345', got %q", keys["conn-1"])
		}
		if keys["conn-2"] != "tok-ant-67890" {
			t.Errorf("expected conn-2 key 'tok-ant-67890', got %q", keys["conn-2"])
		}
	})

	t.Run("returns empty list for provider with no keys", func(t *testing.T) {
		r := chi.NewRouter()
		r.Get("/api/providers/{id}/export-keys", handler.HandleExportProviderKeys)

		req := httptest.NewRequest(http.MethodGet, "/api/providers/emptyprovider/export-keys", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}

		var res struct {
			Connections []any `json:"connections"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		if len(res.Connections) != 0 {
			t.Errorf("expected 0 connections, got %d", len(res.Connections))
		}
	})
}
