package chat

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

// seedCustomPromptDB builds a DB with settings (global system prompt on) and two
// combos: one with customPrompt set, one without. A single openai connection
// points at the given fake upstream.
func seedCustomPromptDB(t *testing.T, upstream *httptest.Server) (*db.Repo, func()) {
	t.Helper()
	database, cleanup := setupChatTestDB(t)

	// Global system prompt enabled with prefix/suffix.
	settingsData := `{"systemPromptEnabled":true,"systemPromptPrefix":"GLOBAL PREFIX","systemPromptSuffix":"GLOBAL SUFFIX"}`
	if _, err := database.Exec(`INSERT INTO settings (id, data) VALUES (1, ?)`, settingsData); err != nil {
		cleanup()
		t.Fatalf("seed settings: %v", err)
	}

	data, _ := json.Marshal(map[string]any{"apiKey": "sk-test", "baseUrl": upstream.URL})
	database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES ('conn-custom-prompt', 'openai', 'apikey', 'Test', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(data))

	modelsJSON := `["openai/gpt-4o"]`
	if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, customPrompt, createdAt, updatedAt) VALUES ('cp1', 'combo-with-prompt', 'fallback', ?, 'CUSTOM SYSTEM PROMPT', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, modelsJSON); err != nil {
		cleanup()
		t.Fatalf("seed combo-with-prompt: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, customPrompt, createdAt, updatedAt) VALUES ('cp2', 'combo-no-prompt', 'fallback', ?, '', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, modelsJSON); err != nil {
		cleanup()
		t.Fatalf("seed combo-no-prompt: %v", err)
	}

	repo := db.NewRepo(database)
	return repo, cleanup
}

// echoUpstream returns an httptest server that records the request body and
// answers 200 with a minimal completion.
func echoUpstream(t *testing.T, captured *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		*captured = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"resp","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
}

// capturedOpenAISystemText extracts the system message text from an OpenAI
// chat-completions request body.
func capturedOpenAISystemText(body string) string {
	var req map[string]any
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		return ""
	}
	arr, _ := req["messages"].([]any)
	for _, m := range arr {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := mm["role"].(string); role == "system" {
			if s, ok := mm["content"].(string); ok {
				return s
			}
		}
	}
	return ""
}

func TestComboCustomPrompt_InjectedWhenSet(t *testing.T) {
	var captured string
	upstream := echoUpstream(t, &captured)
	repo, cleanup := seedCustomPromptDB(t, upstream)
	defer cleanup()

	h := NewChatHandler(repo)
	h.TokenSaver.SetSystemPrompt(true, "GLOBAL PREFIX", "GLOBAL SUFFIX")
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"combo-with-prompt","messages":[{"role":"user","content":"hello"}],"stream":false}`))
	rec := httptest.NewRecorder()
	h.HandleChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(captured, "CUSTOM SYSTEM PROMPT") {
		t.Errorf("combo custom prompt not injected into body:\n%s", captured)
	}
	if strings.Contains(captured, "GLOBAL PREFIX") || strings.Contains(captured, "GLOBAL SUFFIX") {
		t.Errorf("global system prompt should be skipped when combo custom prompt is set:\n%s", captured)
	}
}

func TestComboCustomPrompt_FallsBackToGlobal(t *testing.T) {
	var captured string
	upstream := echoUpstream(t, &captured)
	repo, cleanup := seedCustomPromptDB(t, upstream)
	defer cleanup()

	h := NewChatHandler(repo)
	h.TokenSaver.SetSystemPrompt(true, "GLOBAL PREFIX", "GLOBAL SUFFIX")
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"combo-no-prompt","messages":[{"role":"user","content":"hello"}],"stream":false}`))
	rec := httptest.NewRecorder()
	h.HandleChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}
	systemText := capturedOpenAISystemText(captured)
	if !strings.Contains(systemText, "GLOBAL PREFIX") {
		t.Errorf("global system prompt prefix not applied as fallback:\nsystem=%q body=%s", systemText, captured)
	}
	if !strings.Contains(systemText, "GLOBAL SUFFIX") {
		t.Errorf("global system prompt suffix not applied as fallback:\nsystem=%q body=%s", systemText, captured)
	}
	if strings.Contains(systemText, "CUSTOM SYSTEM PROMPT") {
		t.Errorf("combo custom prompt should not appear when not set on combo:\nsystem=%q", systemText)
	}
}
