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

func TestProcessCommandcodeEvent_TextDelta(t *testing.T) {
	state := &commandcodeStreamState{
		responseID: "test-id",
		created:    1000,
	}
	event := map[string]interface{}{"type": "text-delta", "text": "Hello world"}
	chunks := processCommandcodeEvent(event, "text-delta", state)
	if len(chunks) == 0 {
		t.Fatal("expected output chunks")
	}
	if !strings.Contains(chunks[0], "Hello world") {
		t.Errorf("expected content in chunk, got %s", chunks[0])
	}
	if state.outputLength != 11 {
		t.Errorf("expected outputLength 11, got %d", state.outputLength)
	}
	if state.chunkIndex != 1 {
		t.Errorf("expected chunkIndex 1, got %d", state.chunkIndex)
	}
}

func TestProcessCommandcodeEvent_ReasoningDelta(t *testing.T) {
	state := &commandcodeStreamState{responseID: "test-id", created: 1000}
	event := map[string]interface{}{"type": "reasoning-delta", "text": "thinking step by step"}
	chunks := processCommandcodeEvent(event, "reasoning-delta", state)
	if len(chunks) == 0 {
		t.Fatal("expected output chunks")
	}
	if !strings.Contains(chunks[0], "reasoning_content") {
		t.Errorf("expected reasoning_content, got %s", chunks[0])
	}
}

func TestProcessCommandcodeEvent_ToolInputStart(t *testing.T) {
	state := &commandcodeStreamState{responseID: "test-id", created: 1000}
	event := map[string]interface{}{
		"type":     "tool-input-start",
		"id":       "call_123",
		"toolName": "get_weather",
	}
	chunks := processCommandcodeEvent(event, "tool-input-start", state)
	if len(chunks) == 0 {
		t.Fatal("expected output chunks")
	}
	if !strings.Contains(chunks[0], "get_weather") {
		t.Errorf("expected tool name, got %s", chunks[0])
	}
	if state.toolIndex != 1 {
		t.Errorf("expected toolIndex 1, got %d", state.toolIndex)
	}
}

func TestProcessCommandcodeEvent_ToolInputDelta(t *testing.T) {
	state := &commandcodeStreamState{responseID: "test-id", created: 1000}
	state.toolIndexByID = map[string]int{"call_123": 0}
	event := map[string]interface{}{
		"type":  "tool-input-delta",
		"id":    "call_123",
		"delta": `{"location":"Jakarta"}`,
	}
	chunks := processCommandcodeEvent(event, "tool-input-delta", state)
	if len(chunks) == 0 {
		t.Fatal("expected output chunks")
	}
	if !strings.Contains(chunks[0], "Jakarta") {
		t.Errorf("expected arguments, got %s", chunks[0])
	}
}

func TestProcessCommandcodeEvent_ToolCall(t *testing.T) {
	state := &commandcodeStreamState{responseID: "test-id", created: 1000}
	event := map[string]interface{}{
		"type":       "tool-call",
		"toolCallId": "call_456",
		"toolName":   "search",
		"input":      map[string]interface{}{"query": "test"},
	}
	chunks := processCommandcodeEvent(event, "tool-call", state)
	if len(chunks) == 0 {
		t.Fatal("expected output chunks")
	}
	if !strings.Contains(chunks[0], "search") {
		t.Errorf("expected function name, got %s", chunks[0])
	}
	if !strings.Contains(chunks[0], "test") {
		t.Errorf("expected input in arguments, got %s", chunks[0])
	}
}

func TestProcessCommandcodeEvent_FinishStep(t *testing.T) {
	state := &commandcodeStreamState{responseID: "test-id", created: 1000, finished: false}
	event := map[string]interface{}{
		"type":          "finish-step",
		"finishReason": "stop",
	}
	chunks := processCommandcodeEvent(event, "finish-step", state)
	if len(chunks) != 0 {
		t.Errorf("expected no chunks from finish-step, got %d", len(chunks))
	}
	if state.finishReason != "stop" {
		t.Errorf("expected finishReason 'stop', got %q", state.finishReason)
	}
}

func TestProcessCommandcodeEvent_Finish(t *testing.T) {
	state := &commandcodeStreamState{
		responseID:   "test-id",
		created:      1000,
		finishReason: "stop",
		finished:     false,
	}
	event := map[string]interface{}{"type": "finish"}
	chunks := processCommandcodeEvent(event, "finish", state)
	if len(chunks) == 0 {
		t.Fatal("expected final chunk")
	}
	if !strings.Contains(chunks[0], `"finish_reason":"stop"`) {
		t.Errorf("expected finish_reason, got %s", chunks[0])
	}
	if !state.finished {
		t.Error("expected state.finished = true")
	}
}

func TestProcessCommandcodeEvent_Error(t *testing.T) {
	state := &commandcodeStreamState{responseID: "test-id", created: 1000, finished: false}
	event := map[string]interface{}{"type": "error", "error": "rate limit exceeded"}
	chunks := processCommandcodeEvent(event, "error", state)
	if len(chunks) < 2 {
		t.Fatal("expected error + finish chunks")
	}
	if !strings.Contains(chunks[0], "rate limit exceeded") {
		t.Errorf("expected error message, got %s", chunks[0])
	}
	if !state.finished {
		t.Error("expected state.finished = true after error")
	}
}

func TestProcessCommandcodeEvent_UnknownType(t *testing.T) {
	state := &commandcodeStreamState{responseID: "test-id", created: 1000}
	event := map[string]interface{}{"type": "start"} // should be silently ignored
	chunks := processCommandcodeEvent(event, "start", state)
	if len(chunks) != 0 {
		t.Errorf("expected no chunks for unknown events, got %d", len(chunks))
	}
}

func TestProcessCommandcodeEvent_EmptyText(t *testing.T) {
	state := &commandcodeStreamState{responseID: "test-id", created: 1000}
	event := map[string]interface{}{"type": "text-delta", "text": ""}
	chunks := processCommandcodeEvent(event, "text-delta", state)
	if len(chunks) != 0 {
		t.Errorf("expected no chunks for empty text, got %d", len(chunks))
	}
}

func TestBuildCommandcodeChunk(t *testing.T) {
	state := &commandcodeStreamState{
		responseID: "cmpl-test",
		created:    1000,
		model:      "test-model",
	}
	chunk := buildCommandcodeChunk(state, map[string]interface{}{"content": "hi"}, "")
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(chunk), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed["id"] != "cmpl-test" {
		t.Errorf("expected id, got %v", parsed["id"])
	}
}

func TestHandleCommandcodeStream_Basic(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	upstream := io.NopCloser(strings.NewReader(
		`{"type":"text-delta","text":"Hello"}` + "\n" +
			`{"type":"text-delta","text":" World"}` + "\n" +
			`{"type":"finish","finishReason":"stop"}` + "\n",
	))

	rec := httptest.NewRecorder()
	err := h.handleCommandcodeStream(rec, upstream, "test-model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Hello") || !strings.Contains(body, "World") {
		t.Errorf("expected text chunks, got %s", body)
	}
	if !strings.Contains(body, "[DONE]") {
		t.Errorf("expected [DONE], got %s", body)
	}
}

func TestHandleCommandcodeStream_SSEPrefix(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	// CommandCode may also wrap in SSE "data:" prefix
	upstream := io.NopCloser(strings.NewReader(
		"data: {\"type\":\"text-delta\",\"text\":\"hi\"}\n\n",
	))

	rec := httptest.NewRecorder()
	err := h.handleCommandcodeStream(rec, upstream, "test-model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "hi") {
		t.Errorf("expected content, got %s", rec.Body.String())
	}
}

func TestForwardCommandcodeRequest_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-command-code-version") != "0.25.7" {
			t.Errorf("expected command-code version header, got %q", r.Header.Get("x-command-code-version"))
		}
		if r.Header.Get("x-session-id") == "" {
			t.Errorf("expected x-session-id header")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"type":"text-delta","text":"commandcode response"}` + "\n" +
			`{"type":"finish","finishReason":"stop"}` + "\n"))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	cfg := &providers.ProviderConfig{
		BaseURL: srv.URL,
	}
	body := []byte(`{"model":"deepseek-v4","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	err := h.forwardCommandcodeRequest(rec, cfg, "sk-cc", body, true, false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "commandcode response") {
		t.Errorf("expected response content, got %s", rec.Body.String())
	}
}

func TestForwardCommandcodeRequest_UpstreamError(t *testing.T) {
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
	err := h.forwardCommandcodeRequest(rec, cfg, "bad-key", body, true, false, nil)
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
