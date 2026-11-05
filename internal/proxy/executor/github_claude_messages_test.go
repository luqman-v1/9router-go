package executor

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
)

const claudeMessagesResponse = `{
	"id": "msg_01",
	"type": "message",
	"role": "assistant",
	"model": "claude-3-7-sonnet",
	"content": [{"type": "text", "text": "hello"}],
	"stop_reason": "end_turn",
	"usage": {"input_tokens": 5, "output_tokens": 3}
}`

func TestForwardGithubClaudeMessagesNonStream(t *testing.T) {
	var gotPath, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(claudeMessagesResponse))
	}))
	defer server.Close()

	cfg := &providers.ProviderConfig{
		BaseURL:    server.URL,
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
		StaticHeaders: map[string]string{
			"copilot-integration-id": "copilot-developer-cli",
		},
	}
	req := &Request{
		Ctx:           t.Context(),
		Client:        server.Client(),
		Provider:      "github",
		Config:        cfg,
		APIKey:        "test-key",
		ModelName:     "claude-3-7-sonnet",
		Body:          []byte(`{"model":"claude-3-7-sonnet","messages":[{"role":"user","content":"hi"}]}`),
		IsStream:      false,
		TranslateResp: false,
	}

	rec := httptest.NewRecorder()
	err := ForwardGithub(rec, req)
	if err != nil {
		t.Fatalf("ForwardGithub: %v", err)
	}

	if !strings.HasSuffix(gotPath, "/v1/messages") {
		t.Errorf("expected /v1/messages path, got %q", gotPath)
	}
	if !strings.Contains(gotBody, `"model":"claude-3-7-sonnet"`) {
		t.Errorf("expected Claude model in body, got %q", gotBody)
	}
	if !strings.Contains(gotBody, `"max_tokens"`) {
		t.Errorf("expected max_tokens injected into Claude body, got %q", gotBody)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out["object"] != "chat.completion" {
		t.Errorf("expected OpenAI chat.completion object, got %v", out["object"])
	}
}

func TestForwardGithubRoutesNonClaudeToOpenAI(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	cfg := &providers.ProviderConfig{
		BaseURL: server.URL + "/chat/completions",
	}
	req := &Request{
		Ctx:       t.Context(),
		Client:    server.Client(),
		Provider:  "github",
		Config:    cfg,
		APIKey:    "test-key",
		ModelName: "gpt-4o",
		Body:      []byte(`{"model":"gpt-4o","messages":[]}`),
		IsStream:  false,
	}

	rec := httptest.NewRecorder()
	if err := ForwardGithub(rec, req); err != nil {
		t.Fatalf("ForwardGithub: %v", err)
	}
	if !strings.Contains(gotPath, "/chat/completions") {
		t.Errorf("expected /chat/completions path, got %q", gotPath)
	}
}

func TestIsClaudeModel(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		{"claude-3-7-sonnet", true},
		{"claude-3-5-sonnet", true},
		{"claude-opus-4-1", true},
		{"claude-haiku-3-5", true},
		{"github/claude-3-7-sonnet", true},
		{"gh/claude-opus-4-1", true},
		{"gpt-4o", false},
		{"claude", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isClaudeModel(tc.model); got != tc.want {
			t.Errorf("isClaudeModel(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
}

func TestGithubModel(t *testing.T) {
	req := &Request{ModelName: "claude-3-7-sonnet"}
	model, ok := githubModel(req)
	if !ok || model != "claude-3-7-sonnet" {
		t.Errorf("githubModel() = %q, %v, want %q, true", model, ok, "claude-3-7-sonnet")
	}

	req = &Request{Body: []byte(`{"model":"claude-opus-4-1"}`)}
	model, ok = githubModel(req)
	if !ok || model != "claude-opus-4-1" {
		t.Errorf("githubModel() = %q, %v, want %q, true", model, ok, "claude-opus-4-1")
	}

	req = &Request{Body: []byte(`{"messages":[]}`)}
	if _, ok := githubModel(req); ok {
		t.Errorf("githubModel() with no model wanted ok=false, got true")
	}
}
