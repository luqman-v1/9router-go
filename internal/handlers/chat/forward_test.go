package chat

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy/executor"
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
	err := h.forwardRequest(context.Background(), rec, cfg, "sk-secret", body, false, false, nil, nil, "openai")
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
	err := h.forwardRequest(context.Background(), rec, cfg, "raw-key", body, false, false, nil, nil, "openai")
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
	err := h.forwardRequest(context.Background(), rec, cfg, "k", []byte(`{}`), false, false, nil, nil, "openai")
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
	err := h.forwardRequest(context.Background(), rec, cfg, "should-not-be-sent", []byte(`{}`), false, false, nil, nil, "openai")
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
	err := h.forwardRequest(context.Background(), rec, cfg, "k", []byte(`{}`), false, false, nil, nil, "openai")
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
	err := h.forwardRequest(context.Background(), rec, cfg, "k", []byte(`{}`), false, false, nil, nil, "openai")
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
		// A content chunk first: a stream that opens with the usage trailer and
		// nothing else carries no answer, and the peek now refuses it as the
		// empty turn it is.
		w.Write([]byte("data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n"))
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
	err := h.forwardRequest(ctx, rec, cfg, "k", body, true, false, &streamMetrics{}, nil, "openai")
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

// An upstream that pauses mid-turn for longer than the pending-completion bound
// must still deliver its remaining chunks. Before the redundant stall reader
// was removed, this reader wrapped the upstream at exactly that bound, so a
// pause the bridge tolerates ended the stream early instead.
func TestHandleStreamResponse_ActivePauseExceedsCompletionBound(t *testing.T) {
	prev := executor.PendingCompletionFlushTimeout
	executor.PendingCompletionFlushTimeout = 40 * time.Millisecond
	defer func() { executor.PendingCompletionFlushTimeout = prev }()

	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte(`data: {"id":"c1","choices":[{"index":0,"delta":{"content":"chunk1"}}]}` + "\n\n"))
		time.Sleep(100 * time.Millisecond)
		_, _ = pw.Write([]byte(`data: {"id":"c1","choices":[{"index":0,"delta":{"content":"chunk2"},"finish_reason":"stop"}]}` + "\n\n"))
		_, _ = pw.Write([]byte("data: [DONE]\n\n"))
		_ = pw.Close()
	}()

	rec := httptest.NewRecorder()
	ctx := translator.WithResponsesBridge(translator.WithRequestedModel(context.Background(), "gpt-x"))
	metrics := &streamMetrics{}

	if err := h.handleStreamResponse(ctx, rec, pr, false, time.Now(), metrics); err != nil {
		t.Fatalf("handleStreamResponse failed on active pause: %v", err)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "chunk1") || !strings.Contains(out, "chunk2") {
		t.Errorf("missing streamed chunks in response: %s", out)
	}
	if !strings.Contains(out, "event: response.completed") {
		t.Errorf("expected response.completed in response: %s", out)
	}
}

func TestForwardRequest_NousWireFormat(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	cfg := &providers.ProviderConfig{
		BaseURL:    srv.URL,
		AuthHeader: constants.HeaderAuthorization,
	}

	t.Run("reasoning level high", func(t *testing.T) {
		body := []byte(`{"model":"meituan/longcat-2.0:free","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`)
		rec := httptest.NewRecorder()
		if err := h.forwardRequest(context.Background(), rec, cfg, "test-key", body, false, false, nil, nil, "nous"); err != nil {
			t.Fatalf("forwardRequest: %v", err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(capturedBody, &parsed); err != nil {
			t.Fatalf("unmarshal captured wire body: %v", err)
		}
		if _, exists := parsed["reasoning_effort"]; exists {
			t.Errorf("reasoning_effort should be removed from wire request, got %s", capturedBody)
		}
		reasoning, ok := parsed["reasoning"].(map[string]any)
		if !ok {
			t.Fatalf("expected nested reasoning on wire, got %s", capturedBody)
		}
		if effort, _ := reasoning["effort"].(string); effort != "high" {
			t.Errorf("expected reasoning.effort=high, got %v", reasoning["effort"])
		}
	})

	t.Run("thinking disabled", func(t *testing.T) {
		body := []byte(`{"model":"meituan/longcat-2.0:free","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"}`)
		rec := httptest.NewRecorder()
		if err := h.forwardRequest(context.Background(), rec, cfg, "test-key", body, false, false, nil, nil, "nous"); err != nil {
			t.Fatalf("forwardRequest: %v", err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(capturedBody, &parsed); err != nil {
			t.Fatalf("unmarshal captured wire body: %v", err)
		}
		if _, exists := parsed["reasoning_effort"]; exists {
			t.Errorf("reasoning_effort should be removed from wire request, got %s", capturedBody)
		}
		if _, exists := parsed["reasoning"]; exists {
			t.Errorf("reasoning should be omitted when disabled, got %v", parsed["reasoning"])
		}
	})

	t.Run("reasoning auto mode", func(t *testing.T) {
		body := []byte(`{"model":"meituan/longcat-2.0:free","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"auto"}`)
		rec := httptest.NewRecorder()
		if err := h.forwardRequest(context.Background(), rec, cfg, "test-key", body, false, false, nil, nil, "nous"); err != nil {
			t.Fatalf("forwardRequest: %v", err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(capturedBody, &parsed); err != nil {
			t.Fatalf("unmarshal captured wire body: %v", err)
		}
		if _, exists := parsed["reasoning_effort"]; exists {
			t.Errorf("reasoning_effort should be removed from wire request, got %s", capturedBody)
		}
		reasoning, ok := parsed["reasoning"].(map[string]any)
		if !ok {
			t.Fatalf("expected nested reasoning on wire, got %s", capturedBody)
		}
		if effort, _ := reasoning["effort"].(string); effort != "medium" {
			t.Errorf("expected reasoning.effort=medium, got %v", reasoning["effort"])
		}
	})

	t.Run("attribution tags applied on HTTPS nous endpoint with api key", func(t *testing.T) {
		var interceptedBody []byte
		client := &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				interceptedBody, _ = io.ReadAll(r.Body)
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"ok"}}]}`)),
				}, nil
			}),
		}
		nousCfg := &providers.ProviderConfig{
			BaseURL: "https://inference-api.nousresearch.com/v1/chat/completions",
		}
		body := []byte(`{"model":"stepfun/step-5-preview:free","messages":[{"role":"user","content":"hi"}]}`)
		rec := httptest.NewRecorder()
		if err := h.forwardRequest(context.Background(), rec, nousCfg, "test-key", body, false, false, nil, client, "nous"); err != nil {
			t.Fatalf("forwardRequest: %v", err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(interceptedBody, &parsed); err != nil {
			t.Fatalf("unmarshal wire body: %v", err)
		}
		tags, ok := parsed["tags"].([]any)
		if !ok || len(tags) == 0 {
			t.Fatalf("expected attribution tags on wire, got %v", parsed["tags"])
		}
	})

	t.Run("attribution tags omitted on plaintext downgrade", func(t *testing.T) {
		var interceptedBody []byte
		client := &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				interceptedBody, _ = io.ReadAll(r.Body)
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"ok"}}]}`)),
				}, nil
			}),
		}
		httpCfg := &providers.ProviderConfig{
			BaseURL: "http://inference-api.nousresearch.com/v1",
		}
		body := []byte(`{"model":"stepfun/step-5-preview:free","messages":[{"role":"user","content":"hi"}]}`)
		rec := httptest.NewRecorder()
		if err := h.forwardRequest(context.Background(), rec, httpCfg, "test-key", body, false, false, nil, client, "nous"); err != nil {
			t.Fatalf("forwardRequest: %v", err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(interceptedBody, &parsed); err != nil {
			t.Fatalf("unmarshal wire body: %v", err)
		}
		if _, exists := parsed["tags"]; exists {
			t.Errorf("attribution tags must not be applied on plaintext endpoint, got %v", parsed["tags"])
		}
	})

	t.Run("attribution tags omitted on evil host substring match", func(t *testing.T) {
		var interceptedBody []byte
		client := &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				interceptedBody, _ = io.ReadAll(r.Body)
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"ok"}}]}`)),
				}, nil
			}),
		}
		evilCfg := &providers.ProviderConfig{
			BaseURL: "https://inference-api.nousresearch.com.evil.test/v1",
		}
		body := []byte(`{"model":"stepfun/step-5-preview:free","messages":[{"role":"user","content":"hi"}]}`)
		rec := httptest.NewRecorder()
		if err := h.forwardRequest(context.Background(), rec, evilCfg, "test-key", body, false, false, nil, client, "nous"); err != nil {
			t.Fatalf("forwardRequest: %v", err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(interceptedBody, &parsed); err != nil {
			t.Fatalf("unmarshal wire body: %v", err)
		}
		if _, exists := parsed["tags"]; exists {
			t.Errorf("attribution tags must not be applied on evil host, got %v", parsed["tags"])
		}
	})
}

// TestForwardRequest_NousThinkingNotAppliedToNonNousProvider verifies that the Nous
// thinking dialect is NOT applied when the target provider is not "nous" (AGENTS.md §3.A).
// This prevents cross-provider leakage where a custom OpenAI-compatible node serving
// a model with a matching ID (e.g., meituan/longcat-2.0:free) would incorrectly get
// the Nous nested reasoning format applied.
func TestForwardRequest_NousThinkingNotAppliedToNonNousProvider(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	cfg := &providers.ProviderConfig{
		BaseURL:    srv.URL,
		AuthHeader: constants.HeaderAuthorization,
	}

	t.Run("non-nous provider retains top-level reasoning_effort", func(t *testing.T) {
		body := []byte(`{"model":"meituan/longcat-2.0:free","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`)
		rec := httptest.NewRecorder()
		if err := h.forwardRequest(context.Background(), rec, cfg, "test-key", body, false, false, nil, nil, "openai-compatible-chat-x"); err != nil {
			t.Fatalf("forwardRequest: %v", err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(capturedBody, &parsed); err != nil {
			t.Fatalf("unmarshal captured wire body: %v", err)
		}
		// Top-level reasoning_effort should be preserved for non-nous providers
		if effort, _ := parsed["reasoning_effort"].(string); effort != "high" {
			t.Errorf("expected reasoning_effort=high preserved for non-nous provider, got %v", parsed["reasoning_effort"])
		}
		// Nested reasoning object should NOT be added for non-nous providers
		if _, exists := parsed["reasoning"]; exists {
			t.Errorf("expected reasoning NOT added for non-nous provider, but got %v", parsed["reasoning"])
		}
	})

	t.Run("non-nous provider with auto mode retains top-level reasoning_effort", func(t *testing.T) {
		body := []byte(`{"model":"meituan/longcat-2.0:free","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"auto"}`)
		rec := httptest.NewRecorder()
		if err := h.forwardRequest(context.Background(), rec, cfg, "test-key", body, false, false, nil, nil, "openai-compatible-chat-x"); err != nil {
			t.Fatalf("forwardRequest: %v", err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(capturedBody, &parsed); err != nil {
			t.Fatalf("unmarshal captured wire body: %v", err)
		}
		if effort, _ := parsed["reasoning_effort"].(string); effort != "auto" {
			t.Errorf("expected reasoning_effort=auto preserved for non-nous provider, got %v", parsed["reasoning_effort"])
		}
		if _, exists := parsed["reasoning"]; exists {
			t.Errorf("expected reasoning NOT added for non-nous provider, but got %v", parsed["reasoning"])
		}
	})

	t.Run("non-nous provider with thinking disabled retains top-level reasoning_effort", func(t *testing.T) {
		body := []byte(`{"model":"meituan/longcat-2.0:free","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"}`)
		rec := httptest.NewRecorder()
		if err := h.forwardRequest(context.Background(), rec, cfg, "test-key", body, false, false, nil, nil, "openai-compatible-chat-x"); err != nil {
			t.Fatalf("forwardRequest: %v", err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(capturedBody, &parsed); err != nil {
			t.Fatalf("unmarshal captured wire body: %v", err)
		}
		if effort, _ := parsed["reasoning_effort"].(string); effort != "none" {
			t.Errorf("expected reasoning_effort=none preserved for non-nous provider, got %v", parsed["reasoning_effort"])
		}
		if _, exists := parsed["reasoning"]; exists {
			t.Errorf("expected reasoning NOT added for non-nous provider, but got %v", parsed["reasoning"])
		}
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
