package chat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/models"
)

// A custom openai-compatible node is configured with the base URL its provider
// documents, and the common shape carries a vendor segment before the version:
// AnyRouter's is https://anyrouter.dev/api/v1. The chat route has to be added
// behind that prefix, and the models route has to be derived from the same
// completed route — appending "/v1/models" to it produced
// /api/v1/v1/models and left the node with no live catalogue.
func TestCompatibleNodeModelsURLFollowsTheChatRoute(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"vendor-prefixed versioned base", "https://anyrouter.dev/api/v1", "https://anyrouter.dev/api/v1/models"},
		{"trailing slash on a versioned base", "https://anyrouter.dev/api/v1/", "https://anyrouter.dev/api/v1/models"},
		{"bare host gains the v1 lane", "https://api.example.com", "https://api.example.com/v1/models"},
		{"v1 prefix is not doubled", "https://api.example.com/v1", "https://api.example.com/v1/models"},
		{"vendor segment before a version is not doubled", "https://api.example.com/openai/v1", "https://api.example.com/openai/v1/models"},
		{"full chat route is reduced to models", "https://api.example.com/v1/chat/completions", "https://api.example.com/v1/models"},
		{"a named route keeps its own models child", "https://api.example.com/v1/proxy", "https://api.example.com/v1/proxy/models"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CompatibleNodeModelsURL(tt.in); got != tt.want {
				t.Errorf("CompatibleNodeModelsURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// The live catalogue request must land on the same route the chat request
// uses, not on a blind "/v1/models" append, and it must find the endpoint at
// all: a compatible node stores its base URL on the node row, while the
// connection under it carries only the credential.
func TestFetchCompatibleNodeModelsRequestsVersionedModelsRoute(t *testing.T) {
	tests := []struct {
		name        string
		seedNode    bool
		psdOverride bool
	}{
		{name: "endpoint from the provider node", seedNode: true},
		{name: "per-connection override wins", psdOverride: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":[{"id":"anyrouter/auto"},{"id":"deepseek/deepseek-v4.1-flash"}]}`))
			}))
			defer upstream.Close()

			database, cleanup := setupChatTestDB(t)
			defer cleanup()
			const nodeID = "openai-compatible-chat-anyrouter"
			if tt.seedNode {
				nodeData := `{"prefix":"anyrouter","apiType":"chat","baseUrl":"` + upstream.URL + `/api/v1"}`
				if _, err := database.Exec(`INSERT INTO providerNodes (id, type, name, data, createdAt, updatedAt) VALUES
					(?, 'openai-compatible', 'AnyRouter', ?, '2026-10-28T00:00:00Z', '2026-10-28T00:00:00Z')`,
					nodeID, nodeData); err != nil {
					t.Fatalf("seed providerNode: %v", err)
				}
			}
			var psd map[string]any
			if tt.psdOverride {
				psd = map[string]any{"baseUrl": upstream.URL + "/api/v1"}
			}

			handler := NewChatHandler(db.NewRepo(database))
			connData := &shared.ConnectionData{APIKey: "sk-anyrouter", ProviderSpecificData: psd}
			discovered := handler.fetchCompatibleNodeModels(context.Background(),
				&models.ProviderConnection{ID: "conn-anyrouter"}, connData, nodeID)

			if gotPath != "/api/v1/models" {
				t.Errorf("models request path = %q, want /api/v1/models", gotPath)
			}
			if len(discovered) != 2 || discovered[0].ID != "anyrouter/auto" {
				t.Fatalf("discovered = %+v, want the two upstream ids", discovered)
			}
		})
	}
}

// The reported failure: model tests and chats for an AnyRouter connection
// reached https://anyrouter.dev/api/v1 (no route) and got a 404 back, so the
// gateway reported every model as missing. The request must land on the
// chat-completions route the base URL is a prefix of.
func TestChatRequestToVersionedCompatibleBaseURLLandsOnChatCompletions(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","model":"auto",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	const nodeID = "openai-compatible-chat-anyrouter"
	nodeData := `{"prefix":"anyrouter","apiType":"chat","baseUrl":"` + upstream.URL + `/api/v1"}`
	if _, err := database.Exec(`INSERT INTO providerNodes (id, type, name, data, createdAt, updatedAt) VALUES
		(?, 'openai-compatible', 'AnyRouter', ?, '2026-10-28T00:00:00Z', '2026-10-28T00:00:00Z')`,
		nodeID, nodeData); err != nil {
		t.Fatalf("seed providerNode: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-anyrouter', ?, 'apikey', 'AnyRouter', 1, 1, '{"apiKey":"sk-anyrouter"}', '2026-10-28T00:00:00Z', '2026-10-28T00:00:00Z')`,
		nodeID); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	handler := NewChatHandler(db.NewRepo(database))
	body := `{"model":"anyrouter/auto","messages":[{"role":"user","content":"hi"}],"stream":false}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.HandleChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/api/v1/chat/completions" {
		t.Errorf("upstream received path %q, want /api/v1/chat/completions", gotPath)
	}
}
