package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy/executor"
)

func TestForwardKimchiRequest_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-kimchi" {
			t.Errorf("expected Bearer auth")
		}
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

	cfg := &providers.ProviderConfig{
		BaseURL: srv.URL,
	}
	body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi","cache_control":{}}],"anthropic_version":"2023-06-01"}`)
	rec := httptest.NewRecorder()
	err := executor.ForwardKimchi(rec, &executor.Request{
		Client:   srv.Client(),
		Config:   cfg,
		APIKey:   "sk-kimchi",
		Body:     body,
		IsStream: false,
	})
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

	cfg := &providers.ProviderConfig{
		BaseURL: srv.URL,
	}
	body := []byte(`{"model":"kimi-k2.5","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	err := executor.ForwardKimchi(rec, &executor.Request{
		Client:   srv.Client(),
		Config:   cfg,
		APIKey:   "sk-kimchi",
		Body:     body,
		IsStream: true,
	})
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

	cfg := &providers.ProviderConfig{
		BaseURL: srv.URL,
	}
	body := []byte(`{"model":"x","messages":[]}`)
	rec := httptest.NewRecorder()
	err := executor.ForwardKimchi(rec, &executor.Request{
		Client:   srv.Client(),
		Config:   cfg,
		APIKey:   "bad-key",
		Body:     body,
		IsStream: true,
	})
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

func TestCleanKimchiBody_TopLevelDrops(t *testing.T) {
	body := map[string]any{
		"model":              "test",
		"anthropic_version":  "2023-06-01",
		"anthropic_beta":     []any{"tools-2024-05-16"},
		"client_metadata":    "some-data",
		"stop_sequences":     []string{"end"},
		"thinking":           map[string]any{},
		"top_k":              20,
	}
	executor.CleanKimchiBody(body)
	if _, ok := body["anthropic_version"]; ok {
		t.Error("anthropic_version should be deleted")
	}
	if _, ok := body["anthropic_beta"]; ok {
		t.Error("anthropic_beta should be deleted")
	}
	if _, ok := body["client_metadata"]; ok {
		t.Error("client_metadata should be deleted")
	}
	if _, ok := body["stop_sequences"]; ok {
		t.Error("stop_sequences should be deleted")
	}
	if _, ok := body["thinking"]; ok {
		t.Error("thinking should be deleted")
	}
	if _, ok := body["top_k"]; ok {
		t.Error("top_k should be deleted")
	}
}

func TestCleanKimchiBody_SystemMerge(t *testing.T) {
	body := map[string]any{
		"model": "test",
		"system": "You are a helpful assistant",
		"messages": []any{
			map[string]any{"role": "user", "content": "hi"},
		},
		"anthropic_version": "2023-06-01",
	}
	executor.CleanKimchiBody(body)
	if _, ok := body["system"]; ok {
		t.Error("system should be deleted after merge")
	}
	msgs := body["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	m0 := msgs[0].(map[string]any)
	if m0["role"] != "system" {
		t.Errorf("expected first message role system, got %v", m0["role"])
	}
}

func TestCleanKimchiBody_SystemArray(t *testing.T) {
	body := map[string]any{
		"model": "test",
		"system": []any{
			map[string]any{"type": "text", "text": "part1"},
			map[string]any{"type": "text", "text": "part2"},
		},
		"messages": []any{
			map[string]any{"role": "user", "content": "hi"},
		},
	}
	executor.CleanKimchiBody(body)
	msgs := body["messages"].([]any)
	m0 := msgs[0].(map[string]any)
	content, _ := m0["content"].(string)
	if !strings.Contains(content, "part1") || !strings.Contains(content, "part2") {
		t.Errorf("expected merged system content, got %q", content)
	}
}

func TestCleanKimchiBody_ExistingSystemMessage(t *testing.T) {
	body := map[string]any{
		"model": "test",
		"system": "override system",
		"messages": []any{
			map[string]any{"role": "system", "content": "existing system"},
			map[string]any{"role": "user", "content": "hi"},
		},
	}
	executor.CleanKimchiBody(body)
	msgs := body["messages"].([]any)
	m0 := msgs[0].(map[string]any)
	content, _ := m0["content"].(string)
	if !strings.Contains(content, "override system") {
		t.Errorf("expected override system prepended, got %q", content)
	}
}

func TestCleanKimchiBody_StripMessageArtifacts(t *testing.T) {
	body := map[string]any{
		"model": "test",
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": "hi",
				"cache_control": map[string]any{"ephemeral": true},
			},
		},
	}
	executor.CleanKimchiBody(body)
	msgs := body["messages"].([]any)
	m0 := msgs[0].(map[string]any)
	if _, ok := m0["cache_control"]; ok {
		t.Error("cache_control should be stripped from messages")
	}
}

func TestCleanKimchiBody_StripToolArtifacts(t *testing.T) {
	body := map[string]any{
		"model": "test",
		"messages": []any{
			map[string]any{"role": "user", "content": "use a tool"},
		},
		"tools": []any{
			map[string]any{
				"name":          "search",
				"cache_control": map[string]any{"ephemeral": true},
			},
		},
	}
	executor.CleanKimchiBody(body)
	tools := body["tools"].([]any)
	t0 := tools[0].(map[string]any)
	if _, ok := t0["cache_control"]; ok {
		t.Error("cache_control should be stripped from tools")
	}
}

func TestCleanKimchiBody_ReasoningContentLong(t *testing.T) {
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
	executor.CleanKimchiBody(body)
	msgs := body["messages"].([]any)
	m0 := msgs[0].(map[string]any)
	if _, ok := m0["reasoning_content"]; ok {
		t.Error("reasoning_content should be stripped for long values")
	}
}

func TestCleanKimchiBody_ReasoningContentShort(t *testing.T) {
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
	executor.CleanKimchiBody(body)
	msgs := body["messages"].([]any)
	m0 := msgs[0].(map[string]any)
	if _, ok := m0["reasoning_content"]; !ok {
		t.Error("short reasoning_content placeholder should be kept")
	}
}

func TestKimchiSystemToText_String(t *testing.T) {
	result := executor.KimchiSystemToText("be helpful")
	if result != "be helpful" {
		t.Errorf("expected 'be helpful', got %q", result)
	}
}

func TestKimchiSystemToText_Array(t *testing.T) {
	result := executor.KimchiSystemToText([]any{
		map[string]any{"type": "text", "text": "part1"},
		map[string]any{"type": "text", "text": "part2"},
	})
	if !strings.Contains(result, "part1") || !strings.Contains(result, "part2") {
		t.Errorf("expected both parts, got %q", result)
	}
}

func TestKimchiSystemToText_Nil(t *testing.T) {
	result := executor.KimchiSystemToText(nil)
	if result != "" {
		t.Errorf("expected empty, got %q", result)
	}
}
