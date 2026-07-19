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

func TestProcessCodexEvent_OutputTextDelta(t *testing.T) {
	state := &executor.CodexStreamState{}
	data, _ := json.Marshal(map[string]string{
		"type":  "response.output_text.delta",
		"delta": "Hello, ",
	})
	out := executor.ProcessCodexEvent(string(data), state, "test-id", 1000)
	if len(out) == 0 {
		t.Fatal("expected output events")
	}
	if !strings.Contains(out[0], "Hello,") {
		t.Errorf("expected delta content, got %s", out[0])
	}
	if state.OutputLength != 7 {
		t.Errorf("expected outputLength 7, got %d", state.OutputLength)
	}
}

func TestProcessCodexEvent_FunctionCallDelta(t *testing.T) {
	state := &executor.CodexStreamState{}
	data, _ := json.Marshal(map[string]string{
		"type":  "response.function_call_arguments.delta",
		"delta": `{"loc`,
	})
	out := executor.ProcessCodexEvent(string(data), state, "test-id", 1000)
	if len(out) == 0 {
		t.Fatal("expected output events")
	}
	if !strings.Contains(out[0], `"loc`) {
		t.Errorf("expected arguments delta, got %s", out[0])
	}
	if state.ToolCallCount != 1 {
		t.Errorf("expected toolCallCount 1, got %d", state.ToolCallCount)
	}
}

func TestProcessCodexEvent_FunctionCallDone(t *testing.T) {
	state := &executor.CodexStreamState{}
	event, _ := json.Marshal(map[string]string{
		"type":      "response.function_call_arguments.done",
		"name":      "get_weather",
		"arguments": `{"location":"Jakarta"}`,
	})
	out := executor.ProcessCodexEvent(string(event), state, "test-id", 1000)
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
	state := &executor.CodexStreamState{}
	event, _ := json.Marshal(map[string]string{
		"type": "response.completed",
	})
	out := executor.ProcessCodexEvent(string(event), state, "test-id", 1000)
	if len(out) == 0 {
		t.Fatal("expected output events")
	}
	if !strings.Contains(out[0], `"finish_reason":"stop"`) {
		t.Errorf("expected finish_reason stop, got %s", out[0])
	}
}

func TestProcessCodexEvent_UnknownType(t *testing.T) {
	state := &executor.CodexStreamState{}
	event, _ := json.Marshal(map[string]string{
		"type": "response.unknown",
	})
	out := executor.ProcessCodexEvent(string(event), state, "test-id", 1000)
	if len(out) != 0 {
		t.Errorf("expected no output for unknown event, got %v", out)
	}
}

func TestExtractSimpleText_String(t *testing.T) {
	result := executor.ExtractSimpleText(json.RawMessage(`"plain text"`))
	if result != "plain text" {
		t.Errorf("expected 'plain text', got %q", result)
	}
}

func TestExtractSimpleText_EmptyString(t *testing.T) {
	result := executor.ExtractSimpleText(json.RawMessage(`""`))
	if result != "" {
		t.Errorf("expected empty, got %q", result)
	}
}

func TestExtractSimpleText_Blocks(t *testing.T) {
	result := executor.ExtractSimpleText(json.RawMessage(`[{"type":"text","text":"hello from block"}]`))
	if result != "hello from block" {
		t.Errorf("expected 'hello from block', got %q", result)
	}
}

func TestExtractSimpleText_InvalidJSON(t *testing.T) {
	result := executor.ExtractSimpleText(json.RawMessage(`not json`))
	if result != "" {
		t.Errorf("expected empty for invalid, got %q", result)
	}
}

func TestForwardCodexRequest_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("originator") != "codex_cli_rs" {
			t.Errorf("expected originator header")
		}
		if r.Header.Get("Authorization") != "Bearer sk-codex" {
			t.Errorf("expected Bearer auth")
		}

		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("parse body: %v", err)
		}
		input, ok := reqBody["input"].([]interface{})
		if !ok || len(input) == 0 {
			t.Errorf("expected Responses API input array")
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

	cfg := &providers.ProviderConfig{
		BaseURL: srv.URL,
	}
	body := []byte(`{"model":"gpt-4o-codex","messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	err := executor.ForwardCodex(rec, &executor.Request{
		Client:   srv.Client(),
		Config:   cfg,
		APIKey:   "sk-codex",
		Body:     body,
		IsStream: true,
	})
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

	cfg := &providers.ProviderConfig{
		BaseURL: srv.URL,
	}
	body := []byte(`{"model":"x","messages":[]}`)
	rec := httptest.NewRecorder()
	err := executor.ForwardCodex(rec, &executor.Request{
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
