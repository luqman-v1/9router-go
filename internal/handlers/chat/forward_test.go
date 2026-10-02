package chat

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/translator"
)

// setupHandlerForForward wires a ChatHandler to a temp DB (no connections needed for forward tests).
func setupHandlerForForward(t *testing.T) (*ChatHandler, func()) {
	t.Helper()
	database, cleanup := setupChatTestDB(t)
	repo := db.NewRepo(database)
	return NewChatHandler(repo), cleanup
}

// openaiOKServer returns a fixed JSON chat completion. The upstream request's
// Authorization header is echoed back inside the response body (as "auth=<value>")
// because forwardRequest does not copy upstream response headers onto the client
// recorder — only the body survives the round trip.
func openaiOKServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get(constants.HeaderAuthorization)
		custom := r.Header.Get("X-Custom-Auth")
		foo := r.Header.Get("X-Foo")
		accept := r.Header.Get("Accept")
		body := `{"id":"resp","choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":1,"completion_tokens":1},`
		body += `"_echo":{"auth":` + quote(auth) + `,"custom":` + quote(custom) + `,"foo":` + quote(foo) + `,"accept":` + quote(accept) + `}}`
		w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(body))
	}))
}

func quote(s string) string {
	return `"` + s + `"`
}

func TestForwardRequest_BearerAuth(t *testing.T) {
	srv := openaiOKServer()
	defer srv.Close()

	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	cfg := &providers.ProviderConfig{
		BaseURL:       srv.URL,
		AuthScheme:    constants.AuthSchemeBearer,
		AuthHeader:    constants.HeaderAuthorization,
		StaticHeaders: map[string]string{},
	}

	rec := httptest.NewRecorder()
	body := []byte(`{"model":"x","messages":[{"role":"user","content":"hi"}]}`)
	err := h.forwardRequest(context.Background(), rec, cfg, "sk-secret", body, false, false, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"auth":"Bearer sk-secret"`) {
		t.Errorf("expected bearer auth echoed, got %s", rec.Body.String())
	}
}

func TestForwardRequest_RawAuth(t *testing.T) {
	srv := openaiOKServer()
	defer srv.Close()

	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	cfg := &providers.ProviderConfig{
		BaseURL:       srv.URL,
		AuthScheme:    constants.AuthSchemeRaw,
		AuthHeader:    "X-Custom-Auth",
		StaticHeaders: map[string]string{},
	}

	rec := httptest.NewRecorder()
	body := []byte(`{"model":"x","messages":[]}`)
	err := h.forwardRequest(context.Background(), rec, cfg, "raw-key", body, false, false, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(rec.Body.String(), `"custom":"raw-key"`) {
		t.Errorf("expected raw auth echoed, got %s", rec.Body.String())
	}
}

func TestForwardRequest_DefaultAuthWhenUnknownScheme(t *testing.T) {
	srv := openaiOKServer()
	defer srv.Close()

	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	cfg := &providers.ProviderConfig{
		BaseURL:       srv.URL,
		AuthScheme:    "weird",
		AuthHeader:    "X-Weird",
		StaticHeaders: map[string]string{},
	}

	rec := httptest.NewRecorder()
	err := h.forwardRequest(context.Background(), rec, cfg, "k", []byte(`{}`), false, false, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(rec.Body.String(), `"auth":"Bearer k"`) {
		t.Errorf("expected default bearer auth echoed, got %s", rec.Body.String())
	}
}

func TestForwardRequest_NoAuth(t *testing.T) {
	srv := openaiOKServer()
	defer srv.Close()

	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	cfg := &providers.ProviderConfig{
		BaseURL:       srv.URL,
		NoAuth:        true,
		AuthHeader:    constants.HeaderAuthorization,
		StaticHeaders: map[string]string{},
	}

	rec := httptest.NewRecorder()
	err := h.forwardRequest(context.Background(), rec, cfg, "should-not-be-sent", []byte(`{}`), false, false, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(rec.Body.String(), "should-not-be-sent") {
		t.Errorf("expected no auth sent when NoAuth, got %s", rec.Body.String())
	}
}

func TestForwardRequest_StaticHeadersApplied(t *testing.T) {
	srv := openaiOKServer()
	defer srv.Close()

	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	cfg := &providers.ProviderConfig{
		BaseURL:       srv.URL,
		AuthHeader:    constants.HeaderAuthorization,
		StaticHeaders: map[string]string{"X-Foo": "bar"},
	}

	rec := httptest.NewRecorder()
	err := h.forwardRequest(context.Background(), rec, cfg, "k", []byte(`{}`), false, false, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(rec.Body.String(), `"foo":"bar"`) {
		t.Errorf("expected static header echoed, got %s", rec.Body.String())
	}
}

func TestForwardRequest_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer srv.Close()

	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	cfg := &providers.ProviderConfig{
		BaseURL:       srv.URL,
		AuthHeader:    constants.HeaderAuthorization,
		StaticHeaders: map[string]string{},
	}

	rec := httptest.NewRecorder()
	err := h.forwardRequest(context.Background(), rec, cfg, "k", []byte(`{}`), false, false, nil, nil)
	if err == nil {
		t.Fatal("expected upstream error")
	}
	var ue *upstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *upstreamError, got %T", err)
	}
	if ue.StatusCode != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", ue.StatusCode)
	}
	if !bytes.Contains(ue.Body, []byte("rate limited")) {
		t.Errorf("expected body to contain upstream error, got %s", ue.Body)
	}
}

func TestHandleJSONResponse_NonTranslate(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	upstream := strings.NewReader(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`)
	rec := httptest.NewRecorder()
	if err := h.handleJSONResponse(context.Background(), rec, upstream, false, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if rec.Body.String() != `{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}` {
		t.Errorf("expected passthrough body, got %s", rec.Body.String())
	}
}

func TestHandleJSONResponse_Translate(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	// Non-stream: handleJSONResponse calls TranslateOpenAIToClaude (expects raw JSON, not SSE).
	upstream := strings.NewReader(`{"id":"chatcmpl-test","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	rec := httptest.NewRecorder()
	if err := h.handleJSONResponse(context.Background(), rec, upstream, true, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	// Translated output should be Claude message JSON containing content blocks.
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"message"`) {
		t.Errorf("expected Claude message JSON, got %s", body)
	}
	if !strings.Contains(body, `"text":"hi"`) {
		t.Errorf("expected text content, got %s", body)
	}
}

func TestHandleStreamResponse_NonTranslate(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	upstream := strings.NewReader("data: chunk1\n\ndata: chunk2\n\n")
	rec := httptest.NewRecorder()
	metrics := &streamMetrics{}
	if err := h.handleStreamResponse(context.Background(), rec, upstream, false, time.Now(), metrics); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Header().Get(constants.HeaderContentType) != constants.ContentTypeEventStream {
		t.Errorf("expected event-stream content type, got %q", rec.Header().Get(constants.HeaderContentType))
	}
	if rec.Body.String() != "data: chunk1\n\ndata: chunk2\n\n" {
		t.Errorf("expected raw passthrough, got %q", rec.Body.String())
	}
	if metrics.ResponseBuf.String() != "data: chunk1\n\ndata: chunk2\n\n" {
		t.Errorf("expected accumulated response buffer, got %q", metrics.ResponseBuf.String())
	}
	if metrics.TTFT < 0 {
		t.Errorf("expected non-negative ttft, got %d", metrics.TTFT)
	}
}

func TestHandleStreamResponse_Translate(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	// OpenAI SSE chunk fed to the translator.
	upstream := strings.NewReader(`data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n")
	rec := httptest.NewRecorder()
	metrics := &streamMetrics{}
	if err := h.handleStreamResponse(context.Background(), rec, upstream, true, time.Now(), metrics); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "content_block_delta") {
		t.Errorf("expected translated stream, got %s", rec.Body.String())
	}
}

func TestForwardRequest_StreamSetsAcceptHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(constants.HeaderContentType, constants.ContentTypeEventStream)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("data: {\"id\":\"x\",\"choices\":[],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":2,\"prompt_tokens_details\":{\"cached_tokens\":6}}}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	cfg := &providers.ProviderConfig{
		BaseURL:       srv.URL,
		AuthHeader:    constants.HeaderAuthorization,
		StaticHeaders: map[string]string{},
	}

	body, _ := json.Marshal(map[string]any{"model": "x", "messages": []any{}})
	ctx := translator.WithUsageCapture(context.Background())
	rec := httptest.NewRecorder()
	err := h.forwardRequest(ctx, rec, cfg, "k", body, true, false, &streamMetrics{}, nil)
	if err != nil {
		t.Fatalf("stream request: %v", err)
	}
	usage := translator.GetAndClearUsage(ctx)
	if usage == nil || usage.PromptTokens != 9 || usage.GetCachedTokens() != 6 {
		t.Fatalf("terminal usage not captured: %+v", usage)
	}
	if rec.Header().Get(constants.HeaderContentType) != constants.ContentTypeEventStream {
		t.Errorf("expected event-stream content type on client response, got %q", rec.Header().Get(constants.HeaderContentType))
	}
	if !strings.HasPrefix(rec.Body.String(), "data: {") {
		t.Errorf("expected streamed body, got %q", rec.Body.String())
	}
}
