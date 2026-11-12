package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/providers"
)

func TestForwardCommandcodeGoat_RoutesToMessagesForClaude(t *testing.T) {
	var gotPath, gotAuth, gotVersion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotVersion = r.Header.Get("anthropic-version")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()

	cfg := providers.ProviderConfig{
		BaseURL:    srv.URL,
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
		StaticHeaders: map[string]string{
			"anthropic-version": "2023-06-01",
		},
	}
	req := &Request{
		Ctx:          context.Background(),
		Client:       srv.Client(),
		Config:       &cfg,
		Provider:     "commandcode-goat",
		APIKey:       "user_testkey123",
		ClaudeClient: true,
		Body:         []byte(`{"model":"claude-haiku-5-5","messages":[{"role":"user","content":"hi"}]}`),
	}
	rec := httptest.NewRecorder()
	if err := ForwardCommandcodeGoat(rec, req); err != nil {
		t.Fatalf("ForwardCommandcodeGoat error: %v", err)
	}
	if gotPath != "/messages" {
		t.Errorf("got path %q, want /messages", gotPath)
	}
	if gotAuth != "Bearer user_testkey123" {
		t.Errorf("got auth %q, want Bearer user_testkey123", gotAuth)
	}
	if gotVersion != "2023-06-01" {
		t.Errorf("got anthropic-version %q, want 2023-06-01", gotVersion)
	}
}

func TestForwardCommandcodeGoat_RoutesToChatForOpenModel(t *testing.T) {
	var gotPath, gotAuth, gotZDR string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotZDR = r.Header.Get("x-cmd-zdr")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	cfg := providers.ProviderConfig{
		BaseURL:    srv.URL,
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	}
	req := &Request{
		Ctx:      context.Background(),
		Client:   srv.Client(),
		Config:   &cfg,
		Provider: "commandcode-goat",
		APIKey:   "user_testkey123",
		ConnData: map[string]any{"zdr": true},
		Body:     []byte(`{"model":"zai-org/GLM-5.3","messages":[{"role":"user","content":"hi"}]}`),
	}
	rec := httptest.NewRecorder()
	if err := ForwardCommandcodeGoat(rec, req); err != nil {
		t.Fatalf("ForwardCommandcodeGoat error: %v", err)
	}
	if gotPath != "/chat/completions" {
		t.Errorf("got path %q, want /chat/completions", gotPath)
	}
	if gotAuth != "Bearer user_testkey123" {
		t.Errorf("got auth %q, want Bearer user_testkey123", gotAuth)
	}
	if gotZDR != "1" {
		t.Errorf("got x-cmd-zdr %q, want 1", gotZDR)
	}
}

func TestForwardCommandcodeGoat_StripsProviderPrefix(t *testing.T) {
	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"c1","choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	cfg := providers.ProviderConfig{
		BaseURL: srv.URL,
	}
	for _, raw := range []string{"cmc-goat/zai-org/GLM-5.3", "commandcode-goat/zai-org/GLM-5.3(high)"} {
		req := &Request{
			Ctx:      context.Background(),
			Client:   srv.Client(),
			Config:   &cfg,
			Provider: "commandcode-goat",
			APIKey:   "test",
			Body:     []byte(`{"model":"` + raw + `","messages":[{"role":"user","content":"hi"}]}`),
		}
		gotModel = commandcodeGoatModel(req.Body)
		if gotModel != "zai-org/GLM-5.3" {
			t.Errorf("for %q, gotModel = %q, want zai-org/GLM-5.3", raw, gotModel)
		}
	}
}
