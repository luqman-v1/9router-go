package chat

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	json "encoding/json/v2"

	"9router/proxy/internal/db"
)

// TestHandleChatCompletions_ProviderPreferences drives the feature through
// the real handler: the object both steers resolution and must not leak
// upstream. The mock upstream records the body it received so the second
// assertion is about the wire, not about the parse.
func TestHandleChatCompletions_ProviderPreferences(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	var upstreamBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		upstreamBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"resp-1","choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer upstream.Close()

	customData, _ := json.Marshal(map[string]any{
		"apiKey":  "sk-mock",
		"baseUrl": upstream.URL,
	})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-mock', 'deepseek', 'apikey', 'Mock DeepSeek', 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(customData)); err != nil {
		t.Fatalf("failed to insert mock connection: %v", err)
	}

	handler := NewChatHandler(db.NewRepo(database))

	t.Run("a permitted request forwards without the client-only field", func(t *testing.T) {
		reqBody := `{"model":"deepseek/deepseek-chat","messages":[{"role":"user","content":"hi"}],"stream":false,"provider":{"only":["deepseek"]}}`
		req := httptest.NewRequest("POST", "/chat/completions", strings.NewReader(reqBody))
		rec := httptest.NewRecorder()

		handler.HandleChatCompletions(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
		}
		if strings.Contains(upstreamBody, `"provider"`) {
			t.Errorf("upstream received the client-only provider field: %s", upstreamBody)
		}
		if !strings.Contains(upstreamBody, "deepseek-chat") {
			t.Errorf("upstream body lost the model field: %s", upstreamBody)
		}
	})

	t.Run("an excluded provider is a 400 with the preferences named", func(t *testing.T) {
		reqBody := `{"model":"deepseek/deepseek-chat","messages":[{"role":"user","content":"hi"}],"stream":false,"provider":{"only":["openai"]}}`
		req := httptest.NewRequest("POST", "/chat/completions", strings.NewReader(reqBody))
		rec := httptest.NewRecorder()

		handler.HandleChatCompletions(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "provider preferences") {
			t.Errorf("body = %s, want the error to name the provider preferences", rec.Body.String())
		}
	})
}
