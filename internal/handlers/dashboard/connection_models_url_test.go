package dashboard

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The dashboard's "Import from /models" button reads the same catalogue the
// live model list reads, so it has to complete the node's base URL the same
// way. A custom node is usually configured with a bare host or a versioned
// base, and the node — not the connection — stores it; the connection under it
// carries only the credential.
func TestHandleGetConnectionModels_UsesCompatibleNodeDerivation(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		wantURL string
	}{
		{name: "bare host gains the v1 lane", baseURL: "http://upstream.example", wantURL: "http://upstream.example/v1/models"},
		{name: "vendor-prefixed version is not doubled", baseURL: "http://upstream.example/api/v1", wantURL: "http://upstream.example/api/v1/models"},
		{name: "full chat route is reduced to models", baseURL: "http://upstream.example/v1/chat/completions", wantURL: "http://upstream.example/v1/models"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, cleanup := setupNodeTestDB(t)
			defer cleanup()
			router := setupTestRouter(repo)

			nodeData := `{"prefix":"anyrouter","apiType":"chat","baseUrl":"` + tt.baseURL + `"}`
			if _, err := repo.CreateProviderNode("openai-compatible-chat-anyrouter", "openai-compatible", "AnyRouter", nodeData); err != nil {
				t.Fatalf("seed node: %v", err)
			}
			if _, err := repo.RawDB().Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
				('conn-anyrouter', 'openai-compatible-chat-anyrouter', 'apikey', 'AnyRouter', 1, 1, '{"apiKey":"sk-x"}', '2026-10-28T00:00:00Z', '2026-10-28T00:00:00Z')`); err != nil {
				t.Fatalf("seed connection: %v", err)
			}

			calls := withProbeStub(t, func(_ int, p capturedProbe) (int, []byte, error) {
				if p.url != tt.wantURL {
					t.Errorf("probe url = %q, want %q", p.url, tt.wantURL)
				}
				if got := p.headers.Get("Authorization"); got != "Bearer sk-x" {
					t.Errorf("Authorization = %q, want the connection credential", got)
				}
				return http.StatusOK, []byte(`{"data":[{"id":"anyrouter/auto"}]}`), nil
			})

			req := httptest.NewRequest(http.MethodGet, "/api/providers/conn-anyrouter/models", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			if len(*calls) != 1 {
				t.Fatalf("expected 1 probe, got %d", len(*calls))
			}
		})
	}
}
