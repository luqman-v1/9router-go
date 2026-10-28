package chat

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/db"
)

// DeepSeek thinking mode rejects a request that declares tools but replays
// assistant turns without a `reasoning_content` field (400 "The reasoning_content
// in the thinking mode must be passed back to the API"). Upstream injects a
// placeholder (utils/reasoningContentInjector.js); this drives the real dispatch
// path and asserts the fake DeepSeek upstream receives it, and that real
// reasoning the client replayed survives untouched.
func TestHandleAccountFallback_DeepSeekInjectsReasoningContent(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		captured = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","created":0,"model":"deepseek-flash","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	clearSeededConns(t, database)
	seedConnDB(t, database, "deepseek", "conn-ds-reasoning", "sk-test", srv.URL)

	h := NewChatHandler(db.NewRepo(database))
	body := []byte(`{"model":"deepseek-flash","messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"file contents"},
		{"role":"assistant","content":"done","reasoning_content":"I reasoned about it"},
		{"role":"user","content":"continue"}
	],"tools":[{"type":"function","function":{"name":"read","description":"r","parameters":{"type":"object","properties":{}}}}]}`)

	rec := httptest.NewRecorder()
	if err := h.handleAccountFallback(t.Context(), rec, "deepseek", "deepseek-flash", "", body, false, false, "/v1/chat/completions"); err != nil {
		t.Fatalf("forward: %v", err)
	}

	var req struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(captured, &req); err != nil {
		t.Fatalf("captured upstream body: %v", err)
	}
	injected := req.Messages[1]
	if rc, _ := injected["reasoning_content"].(string); rc != " " {
		t.Errorf("assistant tool-call turn reasoning_content = %q, want placeholder", rc)
	}
	real := req.Messages[3]
	if rc, _ := real["reasoning_content"].(string); rc != "I reasoned about it" {
		t.Errorf("replayed reasoning_content = %q, want preserved", rc)
	}
}

// TestHandleAccountFallback_DeepSeekModelOnForeignProvider: the injector's
// provider/model rules apply to any provider serving a deepseek model id
// (upstream /deepseek/i model rule), so the placeholder must be injected on a
// non-deepseek provider too when the requested model is a DeepSeek one.
func TestHandleAccountFallback_DeepSeekModelOnForeignProvider(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		captured = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","created":0,"model":"deepseek-flash","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	clearSeededConns(t, database)
	seedConnDB(t, database, "sumopod", "conn-sumopod-ds", "sk-test", srv.URL)

	h := NewChatHandler(db.NewRepo(database))
	body := []byte(`{"model":"sumopod/deepseek-flash","messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"file contents"},
		{"role":"user","content":"continue"}
	],"tools":[{"type":"function","function":{"name":"read","description":"r","parameters":{"type":"object","properties":{}}}}]}`)

	rec := httptest.NewRecorder()
	if err := h.handleAccountFallback(t.Context(), rec, "sumopod", "deepseek-flash", "", body, false, false, "/v1/chat/completions"); err != nil {
		t.Fatalf("forward: %v", err)
	}

	var req struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(captured, &req); err != nil {
		t.Fatalf("captured upstream body: %v", err)
	}
	injected := req.Messages[1]
	if rc, _ := injected["reasoning_content"].(string); rc != " " {
		t.Errorf("assistant tool-call turn reasoning_content = %q, want placeholder", rc)
	}
}
