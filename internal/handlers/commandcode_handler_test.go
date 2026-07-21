package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy/executor"
)

func TestProcessCommandcodeEvent_TextDelta(t *testing.T) {
	state := &executor.CommandcodeStreamState{
		ResponseID: "test-id",
		Created:    1000,
	}
	event := map[string]interface{}{"type": "text-delta", "text": "Hello world"}
	chunks := executor.ProcessCommandcodeEvent(event, "text-delta", state)
	if len(chunks) == 0 {
		t.Fatal("expected output chunks")
	}
	if !strings.Contains(chunks[0], "Hello world") {
		t.Errorf("expected content in chunk, got %s", chunks[0])
	}
	if state.OutputLength != 11 {
		t.Errorf("expected outputLength 11, got %d", state.OutputLength)
	}
	if state.ChunkIndex != 1 {
		t.Errorf("expected chunkIndex 1, got %d", state.ChunkIndex)
	}
}

func TestProcessCommandcodeEvent_ReasoningDelta(t *testing.T) {
	state := &executor.CommandcodeStreamState{ResponseID: "test-id", Created: 1000}
	event := map[string]interface{}{"type": "reasoning-delta", "text": "thinking step by step"}
	chunks := executor.ProcessCommandcodeEvent(event, "reasoning-delta", state)
	if len(chunks) == 0 {
		t.Fatal("expected output chunks")
	}
	if !strings.Contains(chunks[0], "reasoning_content") {
		t.Errorf("expected reasoning_content, got %s", chunks[0])
	}
}

func TestProcessCommandcodeEvent_ToolInputStart(t *testing.T) {
	state := &executor.CommandcodeStreamState{ResponseID: "test-id", Created: 1000}
	event := map[string]interface{}{
		"type":     "tool-input-start",
		"id":       "call_123",
		"toolName": "get_weather",
	}
	chunks := executor.ProcessCommandcodeEvent(event, "tool-input-start", state)
	if len(chunks) == 0 {
		t.Fatal("expected output chunks")
	}
	if !strings.Contains(chunks[0], "get_weather") {
		t.Errorf("expected tool name, got %s", chunks[0])
	}
	if state.ToolIndex != 1 {
		t.Errorf("expected toolIndex 1, got %d", state.ToolIndex)
	}
}

func TestProcessCommandcodeEvent_ToolInputDelta(t *testing.T) {
	state := &executor.CommandcodeStreamState{ResponseID: "test-id", Created: 1000}
	state.ToolIndexByID = map[string]int{"call_123": 0}
	event := map[string]interface{}{
		"type":  "tool-input-delta",
		"id":    "call_123",
		"delta": `{"location":"Jakarta"}`,
	}
	chunks := executor.ProcessCommandcodeEvent(event, "tool-input-delta", state)
	if len(chunks) == 0 {
		t.Fatal("expected output chunks")
	}
	if !strings.Contains(chunks[0], "Jakarta") {
		t.Errorf("expected arguments, got %s", chunks[0])
	}
}

func TestProcessCommandcodeEvent_ToolCall(t *testing.T) {
	state := &executor.CommandcodeStreamState{ResponseID: "test-id", Created: 1000}
	event := map[string]interface{}{
		"type":       "tool-call",
		"toolCallId": "call_456",
		"toolName":   "search",
		"input":      map[string]interface{}{"query": "test"},
	}
	chunks := executor.ProcessCommandcodeEvent(event, "tool-call", state)
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
	state := &executor.CommandcodeStreamState{ResponseID: "test-id", Created: 1000}
	event := map[string]interface{}{
		"type":          "finish-step",
		"finishReason": "stop",
	}
	chunks := executor.ProcessCommandcodeEvent(event, "finish-step", state)
	if len(chunks) != 0 {
		t.Errorf("expected no chunks from finish-step, got %d", len(chunks))
	}
	if state.FinishReason != "stop" {
		t.Errorf("expected finishReason 'stop', got %q", state.FinishReason)
	}
}

func TestProcessCommandcodeEvent_Finish(t *testing.T) {
	state := &executor.CommandcodeStreamState{
		ResponseID: "test-id",
		Created:    1000,
	}
	event := map[string]interface{}{"type": "finish", "finishReason": "stop"}
	chunks := executor.ProcessCommandcodeEvent(event, "finish", state)
	if len(chunks) == 0 {
		t.Fatal("expected output from finish")
	}
	if !strings.Contains(chunks[0], `"finish_reason":"stop"`) {
		t.Errorf("expected finish_reason, got %s", chunks[0])
	}
	if !state.Finished {
		t.Error("expected state.Finished=true")
	}
}

func TestProcessCommandcodeEvent_Error(t *testing.T) {
	state := &executor.CommandcodeStreamState{ResponseID: "test-id", Created: 1000}
	event := map[string]interface{}{
		"type":  "error",
		"error": "rate limit exceeded",
	}
	chunks := executor.ProcessCommandcodeEvent(event, "error", state)
	if len(chunks) == 0 {
		t.Fatal("expected error output chunks")
	}
	combined := strings.Join(chunks, "")
	if !strings.Contains(combined, "rate limit exceeded") {
		t.Errorf("expected error message, got %s", combined)
	}
	if !state.Finished {
		t.Error("expected state.Finished=true after error")
	}
}

func TestBuildCommandcodeChunk(t *testing.T) {
	state := &executor.CommandcodeStreamState{ResponseID: "test-id", Created: 1000, Model: "deepseek-v4"}
	result := executor.BuildCommandcodeChunk(state, map[string]interface{}{"content": "hi"}, "stop")
	if !strings.Contains(result, "deepseek-v4") {
		t.Errorf("expected model in chunk, got %s", result)
	}
	if !strings.Contains(result, `"finish_reason":"stop"`) {
		t.Errorf("expected finish_reason, got %s", result)
	}
}

func TestForwardCommandcodeRequest_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-command-code-version") != "0.25.7" {
			t.Errorf("expected x-command-code-version header, got %q", r.Header.Get("x-command-code-version"))
		}
		if r.Header.Get("x-cli-environment") != "cli" {
			t.Errorf("expected x-cli-environment header")
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

	cfg := &providers.ProviderConfig{
		BaseURL: srv.URL,
	}
	body := []byte(`{"model":"deepseek-v4","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	err := executor.ForwardCommandcode(rec, &executor.Request{
		Client:   srv.Client(),
		Config:   cfg,
		APIKey:   "sk-cc",
		Body:     body,
		IsStream: true,
	})
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

	cfg := &providers.ProviderConfig{
		BaseURL: srv.URL,
	}
	body := []byte(`{"model":"x","messages":[]}`)
	rec := httptest.NewRecorder()
	err := executor.ForwardCommandcode(rec, &executor.Request{
		Client:   srv.Client(),
		Config:   cfg,
		APIKey:   "bad-key",
		Body:     body,
		IsStream: true,
	})
	if err == nil {
		t.Fatal("expected error for 401")
	}
	var ue *upstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *upstreamError, got %T", err)
	}
	if ue.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", ue.StatusCode)
	}
}
