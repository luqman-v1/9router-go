package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

// --- processCodexEvent tests ---

func TestProcessCodexEvent_OutputTextDelta(t *testing.T) {
	state := &codexStreamState{}
	data, _ := json.Marshal(map[string]string{
		"type":  "response.output_text.delta",
		"delta": "Hello, ",
	})
	out := processCodexEvent(string(data), state, "test-id", 1000)
	if len(out) == 0 {
		t.Fatal("expected output events")
	}
	if !strings.Contains(out[0], "Hello,") {
		t.Errorf("expected delta content, got %s", out[0])
	}
	if state.outputLength != 7 {
		t.Errorf("expected outputLength 7, got %d", state.outputLength)
	}
}

func TestProcessCodexEvent_FunctionCallDelta(t *testing.T) {
	state := &codexStreamState{}
	data, _ := json.Marshal(map[string]string{
		"type":  "response.function_call_arguments.delta",
		"delta": `{"loc`,
	})
	out := processCodexEvent(string(data), state, "test-id", 1000)
	if len(out) == 0 {
		t.Fatal("expected output events")
	}
	if !strings.Contains(out[0], `"loc`) {
		t.Errorf("expected arguments delta, got %s", out[0])
	}
	if state.toolCallCount != 1 {
		t.Errorf("expected toolCallCount 1, got %d", state.toolCallCount)
	}
}

func TestProcessCodexEvent_FunctionCallDone(t *testing.T) {
	state := &codexStreamState{}
	event, _ := json.Marshal(map[string]string{
		"type":      "response.function_call_arguments.done",
		"name":      "get_weather",
		"arguments": `{"location":"Jakarta"}`,
	})
	out := processCodexEvent(string(event), state, "test-id", 1000)
	if len(out) == 0 {
		t.Fatal("expected output events")
	}
	if !strings.Contains(out[0], "get_weather") {
		t.Errorf("expected function name, got %s", out[0])
	}
	if !strings.Contains(out[0], "Jakarta") {
		t.Errorf("expected arguments in output, got %s", out[0])
	}
}

func TestProcessCodexEvent_ResponseCompleted(t *testing.T) {
	state := &codexStreamState{}
	event, _ := json.Marshal(map[string]string{
		"type": "response.completed",
	})
	out := processCodexEvent(string(event), state, "test-id", 1000)
	if len(out) == 0 {
		t.Fatal("expected output events")
	}
	if !strings.Contains(out[0], `"finish_reason":"stop"`) {
		t.Errorf("expected finish_reason stop, got %s", out[0])
	}
}

func TestProcessCodexEvent_UnknownType(t *testing.T) {
	state := &codexStreamState{}
	event, _ := json.Marshal(map[string]string{
		"type": "response.unknown",
	})
	out := processCodexEvent(string(event), state, "test-id", 1000)
	if len(out) != 0 {
		t.Errorf("expected no output for unknown event, got %v", out)
	}
}

// --- extractSimpleText tests ---

func TestExtractSimpleText_String(t *testing.T) {
	result := extractSimpleText(json.RawMessage(`"plain text"`))
	if result != "plain text" {
		t.Errorf("expected 'plain text', got %q", result)
	}
}

func TestExtractSimpleText_EmptyString(t *testing.T) {
	result := extractSimpleText(json.RawMessage(`""`))
	if result != "" {
		t.Errorf("expected empty, got %q", result)
	}
}

func TestExtractSimpleText_Blocks(t *testing.T) {
	result := extractSimpleText(json.RawMessage(`[{"type":"text","text":"hello from block"}]`))
	if result != "hello from block" {
		t.Errorf("expected 'hello from block', got %q", result)
	}
}

func TestExtractSimpleText_InvalidJSON(t *testing.T) {
	result := extractSimpleText(json.RawMessage(`not-json`))
	if result != "" {
		t.Errorf("expected empty for invalid JSON, got %q", result)
	}
}

func TestExtractSimpleText_NoTextKey(t *testing.T) {
	result := extractSimpleText(json.RawMessage(`[{"type":"image","url":"x.jpg"}]`))
	if result != "" {
		t.Errorf("expected empty when no text key, got %q", result)
	}
}

// --- convertUserContent tests ---

func TestConvertUserContent_String(t *testing.T) {
	result := convertUserContent(json.RawMessage(`"hello user"`))
	content, _ := json.Marshal(result)
	if !strings.Contains(string(content), "hello user") {
		t.Errorf("expected content with text, got %s", content)
	}
}

func TestConvertUserContent_TextBlock(t *testing.T) {
	result := convertUserContent(json.RawMessage(`[{"type":"text","text":"hello"}]`))
	content, _ := json.Marshal(result)
	if !strings.Contains(string(content), "hello") {
		t.Errorf("expected content with text, got %s", content)
	}
}

func TestConvertUserContent_ImageBlock(t *testing.T) {
	result := convertUserContent(json.RawMessage(`[{"type":"image_url","image_url":{"url":"https://example.com/img.jpg"}}]`))
	content, _ := json.Marshal(result)
	if !strings.Contains(string(content), "input_image") {
		t.Errorf("expected input_image type for image, got %s", content)
	}
}

func TestConvertUserContent_MixedBlocks(t *testing.T) {
	result := convertUserContent(json.RawMessage(`[
		{"type":"text","text":"describe this"},
		{"type":"image_url","image_url":{"url":"https://example.com/img.jpg"}}
	]`))
	content, _ := json.Marshal(result)
	if !strings.Contains(string(content), "describe this") {
		t.Errorf("expected text content, got %s", content)
	}
	if !strings.Contains(string(content), "input_image") {
		t.Errorf("expected image content, got %s", content)
	}
}

func TestConvertUserContent_EmptyContent(t *testing.T) {
	result := convertUserContent(json.RawMessage(`[]`))
	content, _ := json.Marshal(result)
	if !strings.Contains(string(content), "...") {
		t.Errorf("expected fallback '...' for empty content, got %s", content)
	}
}

// --- handleCodexStream tests ---

func TestHandleCodexStream_TextOnly(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	upstream := io.NopCloser(strings.NewReader(
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello\"}\n\n" +
			"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\" World\"}\n\n" +
			"event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n" +
			"data: [DONE]\n\n",
	))

	rec := httptest.NewRecorder()
	err := h.handleCodexStream(rec, upstream)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Hello") || !strings.Contains(body, "World") {
		t.Errorf("expected text chunks, got %s", body)
	}
	if !strings.Contains(body, `"finish_reason":"stop"`) {
		t.Errorf("expected finish_reason stop, got %s", body)
	}
	if !strings.Contains(body, "[DONE]") {
		t.Errorf("expected [DONE], got %s", body)
	}
}

func TestHandleCodexStream_ToolCalls(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	upstream := io.NopCloser(strings.NewReader(
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"{\\\"loc\"}\n\n" +
			"event: response.function_call_arguments.done\ndata: {\"type\":\"response.function_call_arguments.done\",\"name\":\"get_weather\",\"arguments\":\"{\\\"location\\\":\\\"Jakarta\\\"}\"}\n\n" +
			"event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n" +
			"data: [DONE]\n\n",
	))

	rec := httptest.NewRecorder()
	err := h.handleCodexStream(rec, upstream)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"function"`) {
		t.Errorf("expected tool calls in output, got %s", body)
	}
	if !strings.Contains(body, "get_weather") {
		t.Errorf("expected function name, got %s", body)
	}
}

func TestHandleCodexStream_EmptyData(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	// Empty data lines after event type should be handled gracefully
	upstream := io.NopCloser(strings.NewReader(
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"test\"}\n\n" +
			"data: [DONE]\n\n",
	))

	rec := httptest.NewRecorder()
	err := h.handleCodexStream(rec, upstream)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "test") {
		t.Errorf("expected text, got %s", rec.Body.String())
	}
}

// --- forwardCodexRequest tests ---

func TestForwardCodexRequest_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check originator header
		if r.Header.Get("originator") != "codex_cli_rs" {
			t.Errorf("expected originator header, got %q", r.Header.Get("originator"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(
			"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"codex response\"}\n\n" +
				"event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	cfg := &providers.ProviderConfig{
		BaseURL: srv.URL,
	}
	body := []byte(`{"model":"gpt-4o-codex","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	err := h.forwardCodexRequest(rec, cfg, "sk-codex", body, true, false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "codex response") {
		t.Errorf("expected response content, got %s", rec.Body.String())
	}
}

func TestForwardCodexRequest_UpstreamError(t *testing.T) {
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
	err := h.forwardCodexRequest(rec, cfg, "bad-key", body, true, false, nil)
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
