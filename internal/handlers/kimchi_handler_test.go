package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

func TestForwardKimchiRequest_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-kimchi" {
			t.Errorf("expected Bearer auth")
		}
		// Verify cleaned body — no anthropic_version
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if _, ok := body["anthropic_version"]; ok {
			t.Errorf("anthropic_version should be stripped")
		}
		if _, ok := body["cache_control"]; ok {
			t.Errorf("top-level cache_control should be stripped")
		}
		if body["model"] != "claude-sonnet-4-6" {
			t.Errorf("expected model claude-sonnet-4-6, got %v", body["model"])
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"resp","choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	cfg := &providers.ProviderConfig{
		BaseURL: srv.URL,
	}
	body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi","cache_control":{}}],"anthropic_version":"2023-06-01"}`)
	rec := httptest.NewRecorder()
	err := h.forwardKimchiRequest(rec, cfg, "sk-kimchi", body, false, false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "hi") {
		t.Errorf("expected response content, got %s", rec.Body.String())
	}
}

func TestForwardKimchiRequest_Stream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	cfg := &providers.ProviderConfig{
		BaseURL: srv.URL,
	}
	body := []byte(`{"model":"kimi-k2.5","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	err := h.forwardKimchiRequest(rec, cfg, "sk-kimchi", body, true, false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "hello") {
		t.Errorf("expected streaming content, got %s", rec.Body.String())
	}
}

func TestForwardKimchiRequest_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	cfg := &providers.ProviderConfig{
		BaseURL: srv.URL,
	}
	body := []byte(`{"model":"x","messages":[]}`)
	rec := httptest.NewRecorder()
	err := h.forwardKimchiRequest(rec, cfg, "bad-key", body, true, false, nil)
	if err == nil {
		t.Fatal("expected error for 401")
	}
	ue, ok := err.(*upstreamError)
	if !ok {
		t.Fatalf("expected *upstreamError, got %T", err)
	}
	if ue.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", ue.StatusCode)
	}
}

// --- cleanKimchiBody unit tests ---

func TestCleanKimchiBody_TopLevelDrops(t *testing.T) {
	body := map[string]any{
		"model":              "test",
		"messages":           []any{map[string]any{"role": "user", "content": "hi"}},
		"anthropic_version":  "2023-06-01",
		"anthropic_beta":     []any{"prompt-caching"},
		"stop_sequences":     []any{"stop"},
		"system":             "be helpful",
		"cache_control":      map[string]any{"ephemeral": true},
	}
	// Note: top-level cache_control is not in kimchiTopLevelDrops but "system" is
	// deleted separately. Clean and check.
	cleanKimchiBody(body)
	if _, ok := body["anthropic_version"]; ok {
		t.Error("anthropic_version should be deleted")
	}
	if _, ok := body["anthropic_beta"]; ok {
		t.Error("anthropic_beta should be deleted")
	}
	if _, ok := body["stop_sequences"]; ok {
		t.Error("stop_sequences should be deleted")
	}
	if _, ok := body["system"]; ok {
		t.Error("system should be deleted")
	}
	// System should be merged into messages
	msgs := body["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (system + user), got %d", len(msgs))
	}
	first := msgs[0].(map[string]any)
	if first["role"] != "system" {
		t.Errorf("expected first message role 'system', got %v", first["role"])
	}
}

func TestCleanKimchiBody_MessageArtifacts(t *testing.T) {
	body := map[string]any{
		"model": "test",
		"messages": []any{
			map[string]any{
				"role":          "user",
				"content":       "hi",
				"cache_control": map[string]any{"ephemeral": true},
			},
			map[string]any{
				"role": "assistant",
				"content": []any{
					map[string]any{"type": "text", "text": "hello", "cache_control": map[string]any{"ephemeral": true}, "signature": "abc123"},
				},
			},
		},
	}
	cleanKimchiBody(body)
	msgs := body["messages"].([]any)

	// First msg: cache_control removed
	m0 := msgs[0].(map[string]any)
	if _, ok := m0["cache_control"]; ok {
		t.Error("cache_control should be removed from message level")
	}

	// Second msg content blocks: cache_control and signature removed
	m1 := msgs[1].(map[string]any)
	blocks := m1["content"].([]any)
	b0 := blocks[0].(map[string]any)
	if _, ok := b0["cache_control"]; ok {
		t.Error("cache_control should be removed from content block")
	}
	if _, ok := b0["signature"]; ok {
		t.Error("signature should be removed from content block")
	}
}

func TestCleanKimchiBody_ToolArtifacts(t *testing.T) {
	body := map[string]any{
		"model": "test",
		"messages": []any{
			map[string]any{"role": "user", "content": "use a tool"},
		},
		"tools": []any{
			map[string]any{
				"type":          "function",
				"function":      map[string]any{"name": "get_weather"},
				"cache_control": map[string]any{"ephemeral": true},
			},
		},
	}
	cleanKimchiBody(body)
	tools := body["tools"].([]any)
	t0 := tools[0].(map[string]any)
	if _, ok := t0["cache_control"]; ok {
		t.Error("cache_control should be removed from tool definition")
	}
}

func TestCleanKimchiBody_SystemMergeExisting(t *testing.T) {
	// System field merges into existing system message
	body := map[string]any{
		"model": "test",
		"messages": []any{
			map[string]any{"role": "system", "content": "existing system"},
			map[string]any{"role": "user", "content": "hi"},
		},
		"system": "top-level system",
	}
	cleanKimchiBody(body)
	msgs := body["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	first := msgs[0].(map[string]any)
	content, _ := first["content"].(string)
	if !strings.Contains(content, "top-level system") || !strings.Contains(content, "existing system") {
		t.Errorf("expected merged system content, got %q", content)
	}
}

func TestCleanKimchiBody_ReasoningContent(t *testing.T) {
	body := map[string]any{
		"model": "test",
		"messages": []any{
			map[string]any{
				"role":             "assistant",
				"content":          "final answer",
				"reasoning_content": "long reasoning trace here that is more than 8 chars",
			},
		},
	}
	cleanKimchiBody(body)
	msgs := body["messages"].([]any)
	m0 := msgs[0].(map[string]any)
	if _, ok := m0["reasoning_content"]; ok {
		t.Error("reasoning_content should be stripped for long values")
	}
}

func TestCleanKimchiBody_ReasoningContentShort(t *testing.T) {
	// Short reasoning_content (placeholder) should NOT be stripped
	body := map[string]any{
		"model": "test",
		"messages": []any{
			map[string]any{
				"role":             "assistant",
				"content":          "final answer",
				"reasoning_content": " ",
			},
		},
	}
	cleanKimchiBody(body)
	msgs := body["messages"].([]any)
	m0 := msgs[0].(map[string]any)
	if _, ok := m0["reasoning_content"]; !ok {
		t.Error("short reasoning_content placeholder should be kept")
	}
}

func TestKimchiSystemToText_String(t *testing.T) {
	result := kimchiSystemToText("be helpful")
	if result != "be helpful" {
		t.Errorf("expected 'be helpful', got %q", result)
	}
}

func TestKimchiSystemToText_Array(t *testing.T) {
	result := kimchiSystemToText([]any{
		map[string]any{"type": "text", "text": "part1"},
		map[string]any{"type": "text", "text": "part2"},
	})
	if !strings.Contains(result, "part1") || !strings.Contains(result, "part2") {
		t.Errorf("expected both parts, got %q", result)
	}
}

func TestKimchiSystemToText_Nil(t *testing.T) {
	result := kimchiSystemToText(nil)
	if result != "" {
		t.Errorf("expected empty, got %q", result)
	}
}
