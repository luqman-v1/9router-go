package executor

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
)

const responsesSSE = `data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-6.1-terra"}}

data: {"type":"response.output_text.delta","delta":"Hello world"}

data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-6.1-terra","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}

data: [DONE]
`

func TestForwardGithubGPT6ResponsesStream(t *testing.T) {
	var gotPath, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(responsesSSE))
	}))
	defer server.Close()

	cfg := &providers.ProviderConfig{BaseURL: server.URL, AuthHeader: "Authorization", AuthScheme: "bearer"}
	req := &Request{
		Ctx:       t.Context(),
		Client:    server.Client(),
		Provider:  "github",
		Config:    cfg,
		APIKey:    "test-key",
		ModelName: "gpt-6.1-terra",
		Body:      []byte(`{"model":"gpt-6.1-terra","messages":[{"role":"user","content":"hi"}],"stream":true}`),
		IsStream:  true,
	}

	rec := httptest.NewRecorder()
	if err := ForwardGithub(rec, req); err != nil {
		t.Fatalf("ForwardGithub: %v", err)
	}
	if !strings.Contains(gotPath, "/responses") {
		t.Errorf("expected /responses path, got %q", gotPath)
	}
	if strings.Contains(gotBody, "temperature") {
		t.Errorf("expected temperature stripped from wire body, got %q", gotBody)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "chat.completion.chunk") {
		t.Errorf("expected chat.completion.chunk, got %q", body)
	}
	if !strings.Contains(body, "Hello world") {
		t.Errorf("expected Hello world, got %q", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Errorf("expected [DONE], got %q", body)
	}
}

func TestForwardGithubGPT6ResponsesNonStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(responsesSSE))
	}))
	defer server.Close()

	cfg := &providers.ProviderConfig{BaseURL: server.URL, AuthHeader: "Authorization", AuthScheme: "bearer"}
	req := &Request{
		Ctx:       t.Context(),
		Client:    server.Client(),
		Provider:  "github",
		Config:    cfg,
		APIKey:    "test-key",
		ModelName: "gpt-6.1-terra",
		Body:      []byte(`{"model":"gpt-6.1-terra","messages":[{"role":"user","content":"hi"}],"stream":false}`),
		IsStream:  false,
	}

	rec := httptest.NewRecorder()
	if err := ForwardGithub(rec, req); err != nil {
		t.Fatalf("ForwardGithub: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("expected application/json, got %q", got)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "chat.completion") {
		t.Errorf("expected chat.completion, got %q", body)
	}
	if !strings.Contains(body, "Hello world") {
		t.Errorf("expected Hello world, got %q", body)
	}
	// Must NOT be raw SSE frames.
	if strings.Contains(body, "data:") {
		t.Errorf("expected folded JSON, not raw SSE, got %q", body)
	}
}

func TestForwardGithubClaudeAndGPT4oStillRoute(t *testing.T) {
	// claude-sonnet-4-5 -> /v1/messages
	var claudePath string
	claudeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claudePath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(claudeMessagesResponse))
	}))
	defer claudeServer.Close()

	cfg := &providers.ProviderConfig{BaseURL: claudeServer.URL, AuthHeader: "Authorization", AuthScheme: "bearer"}
	req := &Request{
		Ctx:       t.Context(),
		Client:    claudeServer.Client(),
		Provider:  "github",
		Config:    cfg,
		APIKey:    "test-key",
		ModelName: "claude-sonnet-4-5",
		Body:      []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`),
		IsStream:  false,
	}
	rec := httptest.NewRecorder()
	if err := ForwardGithub(rec, req); err != nil {
		t.Fatalf("ForwardGithub: %v", err)
	}
	if !strings.HasSuffix(claudePath, "/v1/messages") {
		t.Errorf("expected /v1/messages path for claude, got %q", claudePath)
	}

	// gpt-4o -> /chat/completions
	var openaiPath string
	openaiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		openaiPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`))
	}))
	defer openaiServer.Close()

	cfg2 := &providers.ProviderConfig{BaseURL: openaiServer.URL + "/chat/completions", AuthHeader: "Authorization", AuthScheme: "bearer"}
	req2 := &Request{
		Ctx:       t.Context(),
		Client:    openaiServer.Client(),
		Provider:  "github",
		Config:    cfg2,
		APIKey:    "test-key",
		ModelName: "gpt-4o",
		Body:      []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`),
		IsStream:  false,
	}
	rec2 := httptest.NewRecorder()
	if err := ForwardGithub(rec2, req2); err != nil {
		t.Fatalf("ForwardGithub: %v", err)
	}
	if !strings.Contains(openaiPath, "/chat/completions") {
		t.Errorf("expected /chat/completions path for gpt-4o, got %q", openaiPath)
	}
}

func TestForwardGithubGPT6ResponsesContextOverflow(t *testing.T) {
	errBody := []byte(`{"error":{"code":"invalid_request_body","message":"Your input exceeds the context window of this model.","type":"invalid_request_error"}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(errBody)
	}))
	defer server.Close()

	cfg := &providers.ProviderConfig{BaseURL: server.URL, AuthHeader: "Authorization", AuthScheme: "bearer"}
	req := &Request{
		Ctx:       t.Context(),
		Client:    server.Client(),
		Provider:  "github",
		Config:    cfg,
		APIKey:    "test-key",
		ModelName: "gpt-6.1-terra",
		Body:      []byte(`{"model":"gpt-6.1-terra","messages":[{"role":"user","content":"hi"}],"stream":true}`),
		IsStream:  true,
	}

	rec := httptest.NewRecorder()
	err := ForwardGithub(rec, req)
	if err == nil {
		t.Fatal("expected error for context overflow")
	}
	var ue *proxy.UpstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *proxy.UpstreamError, got %T", err)
	}
	if ue.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", ue.StatusCode)
	}
	if !strings.Contains(string(ue.Body), "context_length_exceeded") {
		t.Errorf("expected context_length_exceeded, got %q", string(ue.Body))
	}
}
