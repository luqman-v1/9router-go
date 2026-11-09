package chat

import (
	json "encoding/json/v2"
	"errors"
	"io"
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
	event := map[string]any{"type": "text-delta", "text": "Hello world"}
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
	event := map[string]any{"type": "reasoning-delta", "text": "thinking step by step"}
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
	event := map[string]any{
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
	event := map[string]any{
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
	event := map[string]any{
		"type":       "tool-call",
		"toolCallId": "call_456",
		"toolName":   "search",
		"input":      map[string]any{"query": "test"},
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
	event := map[string]any{
		"type":         "finish-step",
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
	event := map[string]any{"type": "finish", "finishReason": "stop"}
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
	event := map[string]any{
		"type":  "error",
		"error": "rate limit exceeded",
	}
	chunks := executor.ProcessCommandcodeEvent(event, "error", state)
	if len(chunks) != 0 {
		t.Errorf("error must not stream fake content, got %s", strings.Join(chunks, ""))
	}
	if state.ErrorEvent == nil {
		t.Fatal("expected ErrorEvent recorded")
	}
	if !state.Finished {
		t.Error("expected state.Finished=true after error")
	}
}

func TestBuildCommandcodeChunk(t *testing.T) {
	state := &executor.CommandcodeStreamState{ResponseID: "test-id", Created: 1000, Model: "deepseek-v4"}
	result := executor.BuildCommandcodeChunk(state, map[string]any{"content": "hi"}, "stop")
	if !strings.Contains(result, "deepseek-v4") {
		t.Errorf("expected model in chunk, got %s", result)
	}
	if !strings.Contains(result, `"finish_reason":"stop"`) {
		t.Errorf("expected finish_reason, got %s", result)
	}
}

func TestForwardCommandcodeRequest_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-command-code-version") != "1.88.0" {
			t.Errorf("expected x-command-code-version header, got %q", r.Header.Get("x-command-code-version"))
		}
		if r.Header.Get("x-cli-environment") != "production" {
			t.Errorf("expected x-cli-environment header")
		}
		if r.Header.Get("x-session-id") == "" {
			t.Errorf("expected x-session-id header")
		}
		if r.Header.Get("User-Agent") != "cli" {
			t.Errorf("expected User-Agent header 'cli', got %q", r.Header.Get("User-Agent"))
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

func TestForwardCommandcodeRequest_StaticHeaders(t *testing.T) {
	var capturedUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"type":"finish","finishReason":"stop"}` + "\n"))
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{
		BaseURL: srv.URL,
		StaticHeaders: map[string]string{
			"User-Agent": "custom-cc-agent/1.0",
		},
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
	if capturedUA != "custom-cc-agent/1.0" {
		t.Errorf("expected User-Agent 'custom-cc-agent/1.0', got %q", capturedUA)
	}
}

func TestForwardCommandcodeRequest_ImageAndReasoningEffort(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &capturedBody)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"type":"finish","finishReason":"stop"}` + "\n"))
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{
		BaseURL: srv.URL,
	}
	body := []byte(`{
		"model": "deepseek-v4-vision",
		"reasoning_effort": "high",
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "what is this?"},
					{"type": "image_url", "image_url": {"url": "data:image/png;base64,iVBORw0KGgo="}}
				]
			}
		]
	}`)
	rec := httptest.NewRecorder()
	err := executor.ForwardCommandcode(rec, &executor.Request{
		Client:   srv.Client(),
		Config:   cfg,
		APIKey:   "sk-test",
		Body:     body,
		IsStream: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	params, _ := capturedBody["params"].(map[string]any)
	if params == nil {
		t.Fatal("expected params object in payload")
	}
	if effort, _ := params["reasoning_effort"].(string); effort != "high" {
		t.Errorf("expected reasoning_effort 'high', got %v", params["reasoning_effort"])
	}

	msgs, _ := params["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	userMsg, _ := msgs[0].(map[string]any)
	content, _ := userMsg["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(content))
	}
	imgPart, _ := content[1].(map[string]any)
	if imgPart["type"] != "image" {
		t.Errorf("expected content[1].type 'image', got %v", imgPart["type"])
	}
	if imgPart["image"] != "data:image/png;base64,iVBORw0KGgo=" {
		t.Errorf("expected data URI preserved, got %v", imgPart["image"])
	}
	if imgPart["mimeType"] != "image/png" {
		t.Errorf("expected mimeType 'image/png', got %v", imgPart["mimeType"])
	}
}

func TestForwardCommandcodeRequest_NonStreamFoldsToJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"type":"text-delta","text":"commandcode response"}` + "\n" +
			`{"type":"finish","finishReason":"stop"}` + "\n"))
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{BaseURL: srv.URL}
	rec := httptest.NewRecorder()
	// The dashboard's Test button probes with stream:false (probeFor "", Chat
	// Completions). CommandCode always streams, so the executor must fold the
	// frames into one chat.completion instead of answering SSE the JSON parser
	// cannot read.
	err := executor.ForwardCommandcode(rec, &executor.Request{
		Client:        srv.Client(),
		Config:        cfg,
		APIKey:        "sk-test",
		Body:          []byte(`{"model":"cmc/deepseek/deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"stream":false}`),
		IsStream:      false,
		TranslateResp: false,
		Endpoint:      "/v1/chat/completions",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var out struct {
		Object  string `json:"object"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("non-stream response is not JSON: %v\nbody: %s", err, rec.Body.String())
	}
	if out.Object != "chat.completion" {
		t.Errorf("object = %q, want chat.completion", out.Object)
	}
	if len(out.Choices) == 0 {
		t.Fatalf("expected one choice, got none")
	}
	if out.Choices[0].Message.Content != "commandcode response" {
		t.Errorf("content = %q, want %q", out.Choices[0].Message.Content, "commandcode response")
	}
}

func TestForwardCommandcodeRequest_NonStreamEmptyFoldFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"type":"start"}` + "\n"))
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{BaseURL: srv.URL}
	rec := httptest.NewRecorder()
	err := executor.ForwardCommandcode(rec, &executor.Request{
		Client:        srv.Client(),
		Config:        cfg,
		APIKey:        "sk-test",
		Body:          []byte(`{"model":"cmc/deepseek/deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"stream":false}`),
		IsStream:      false,
		TranslateResp: false,
		Endpoint:      "/v1/chat/completions",
	})
	var ue *upstreamError
	if !errors.As(err, &ue) || ue.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502 from a stream with no completion, got %v (err=%v)", rec.Code, err)
	}
}

func TestBuildCommandcodeBody_DefaultsMaxTokens(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &capturedBody)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"type":"text-delta","text":"ok"}` + "\n" +
			`{"type":"finish","finishReason":"stop"}` + "\n"))
	}))
	defer srv.Close()

	cfg := &providers.ProviderConfig{BaseURL: srv.URL}
	rec := httptest.NewRecorder()
	err := executor.ForwardCommandcode(rec, &executor.Request{
		Client:   srv.Client(),
		Config:   cfg,
		APIKey:   "sk-test",
		Body:     []byte(`{"model":"cmc/deepseek/deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"max_tokens":1024,"stream":false}`),
		IsStream: false,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	params, _ := capturedBody["params"].(map[string]any)
	if params == nil {
		t.Fatal("expected params object in payload")
	}
	if mt, _ := params["max_tokens"].(float64); mt != 1024 {
		t.Errorf("expected params.max_tokens 1024 (test ping parity), got %v", params["max_tokens"])
	}
}
