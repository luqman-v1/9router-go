package executor

import (
	json "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/proxy"
)

func parseToolCalls(t *testing.T, sse string) (id string, idx int) {
	t.Helper()
	data := strings.TrimSpace(strings.TrimPrefix(sse, "data: "))
	data = strings.TrimSuffix(data, "\n")
	var chunk struct {
		Choices []struct {
			Delta struct {
				ToolCalls []struct {
					Index int    `json:"index"`
					ID    string `json:"id"`
				} `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		t.Fatalf("unmarshal emitted chunk: %v (got %q)", err, sse)
	}
	if len(chunk.Choices) == 0 || len(chunk.Choices[0].Delta.ToolCalls) == 0 {
		t.Fatalf("no tool_calls in chunk: %q", sse)
	}
	tc := chunk.Choices[0].Delta.ToolCalls[0]
	return tc.ID, tc.Index
}

// Codex streams one tool call's arguments as many
// response.function_call_arguments.delta events; every fragment must carry
// the SAME id/index (the upstream call_id), not a fresh fabricated id per
// fragment — otherwise the client assembles them as separate tools.
func TestProcessCodexEvent_StableToolCallID(t *testing.T) {
	state := &CodexStreamState{}
	for _, delta := range []string{
		`{"type":"response.function_call_arguments.delta","call_id":"call_abc","delta":"{\"path\":\"","name":"Read"}`,
		`{"type":"response.function_call_arguments.delta","call_id":"call_abc","delta":"/tmp\"}","name":"Read"}`,
	} {
		out := ProcessCodexEvent(delta, state, "chatcmpl-x", 1)
		if len(out) == 0 {
			t.Fatalf("expected chunk for delta %q", delta)
		}
		id, idx := parseToolCalls(t, out[0])
		if id != "call_abc" || idx != 0 {
			t.Errorf("delta %q: got id=%q idx=%d, want call_abc/0", delta, id, idx)
		}
	}
}

// A conflicting snapshot cannot replace bytes already sent to a client.
func TestProcessCodexEvent_DoneDoesNotAppendConflictingSnapshot(t *testing.T) {
	state := &CodexStreamState{}
	// First fragment assigns index 0 to call_abc.
	ProcessCodexEvent(`{"type":"response.function_call_arguments.delta","call_id":"call_abc","delta":"{}","name":"Read"}`, state, "chatcmpl-x", 1)
	out := ProcessCodexEvent(`{"type":"response.function_call_arguments.done","call_id":"call_abc","name":"Read","arguments":"{\"path\":\"/tmp\"}"}`, state, "chatcmpl-x", 1)
	if len(out) != 0 {
		t.Errorf("conflicting snapshot was appended to streamed arguments: %v", out)
	}
}

func TestProcessCodexEvent_DoneSuffixKeepsDeltaOnlyToolCallID(t *testing.T) {
	state := &CodexStreamState{}
	ProcessCodexEvent(`{"type":"response.function_call_arguments.delta","call_id":"call_abc","delta":"{\"path\":"}`, state, "chatcmpl-x", 1)
	out := ProcessCodexEvent(`{"type":"response.function_call_arguments.done","call_id":"call_abc","arguments":"{\"path\":\"/tmp\"}"}`, state, "chatcmpl-x", 1)
	if len(out) != 1 {
		t.Fatalf("expected one suffix chunk, got %v", out)
	}
	id, idx := parseToolCalls(t, out[0])
	if id != "call_abc" || idx != 0 {
		t.Errorf("done chunk: got id=%q idx=%d, want call_abc/0", id, idx)
	}
}

func TestProcessCodexEvent_OutputItemDone_NoDuplicateArguments(t *testing.T) {
	// Scenario 1: delta already streamed arguments -> output_item.done must NOT re-emit arguments
	state := &CodexStreamState{}
	deltaOut := ProcessCodexEvent(`{"type":"response.function_call_arguments.delta","call_id":"call_1","delta":"{\"q\":\"*.yaml\"}","name":"search_file"}`, state, "chatcmpl-x", 1)
	if len(deltaOut) == 0 {
		t.Fatal("expected delta chunk")
	}

	doneOut := ProcessCodexEvent(`{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","arguments":"{\"q\":\"*.yaml\"}"}}`, state, "chatcmpl-x", 1)
	if len(doneOut) != 0 {
		t.Fatalf("expected output_item.done to be suppressed after delta, but got %d chunk(s): %v", len(doneOut), doneOut)
	}

	// Scenario 2: no delta was sent (upstream only sent output_item.done) -> MUST emit arguments
	state2 := &CodexStreamState{}
	doneOut2 := ProcessCodexEvent(`{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_2","arguments":"{\"q\":\"*.yaml\"}"}}`, state2, "chatcmpl-x", 1)
	if len(doneOut2) == 0 {
		t.Fatal("expected output_item.done to emit arguments when no delta was streamed")
	}
}

// tool-input-delta must carry the id so the client can associate the
// incremental arguments with the tool call started by tool-input-start.
func TestProcessCommandcodeEvent_ToolInputDeltaHasID(t *testing.T) {
	state := &CommandcodeStreamState{
		ResponseID:    "chatcmpl-c",
		Created:       1,
		Model:         "deepseek",
		ToolIndexByID: map[string]int{"call_1": 0},
	}
	out := ProcessCommandcodeEvent(map[string]any{
		"type":  "tool-input-delta",
		"id":    "call_1",
		"delta": `"path": "/`,
	}, "tool-input-delta", state)
	if len(out) == 0 {
		t.Fatal("expected tool-input-delta chunk")
	}
	id, idx := parseToolCalls(t, out[0])
	if id != "call_1" || idx != 0 {
		t.Errorf("tool-input-delta: got id=%q idx=%d, want call_1/0", id, idx)
	}
}

func TestParseCommandCodeError(t *testing.T) {
	t.Run("parses explicit 503 error event", func(t *testing.T) {
		event := map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":       "server_error",
				"message":    "Service temporarily unavailable. Please try again shortly.",
				"statusCode": float64(503),
			},
		}
		status, msg := ParseCommandCodeError(event)
		if status != 503 {
			t.Errorf("expected 503, got %d", status)
		}
		if !strings.Contains(msg, "Service temporarily unavailable") {
			t.Errorf("unexpected message: %s", msg)
		}
	})

	t.Run("parses rate limit message as 429", func(t *testing.T) {
		event := map[string]any{
			"type": "error",
			"error": map[string]any{
				"message": "Rate limit reached for model",
			},
		}
		status, _ := ParseCommandCodeError(event)
		if status != 429 {
			t.Errorf("expected 429, got %d", status)
		}
	})

	t.Run("parses unauthorized message as 401", func(t *testing.T) {
		event := map[string]any{
			"type": "error",
			"error": map[string]any{
				"message": "Invalid API key provided",
			},
		}
		status, _ := ParseCommandCodeError(event)
		if status != 401 {
			t.Errorf("expected 401, got %d", status)
		}
	})
}

func TestProcessCodexEvent_OutputItemAdded_FunctionCall(t *testing.T) {
	state := &CodexStreamState{}
	// 1. Output item added (function_call with name 'view_file')
	out := ProcessCodexEvent(`{
		"type": "response.output_item.added",
		"item": {
			"id": "item_123",
			"type": "function_call",
			"call_id": "call_abc123",
			"name": "view_file"
		}
	}`, state, "chatcmpl-test", 1)

	if len(out) == 0 {
		t.Fatal("expected chunk for output_item.added")
	}

	var chunk struct {
		Choices []struct {
			Delta struct {
				ToolCalls []struct {
					Index    int    `json:"index"`
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(out[0], "data: ")), &chunk); err != nil {
		t.Fatalf("unmarshal chunk: %v", err)
	}

	if len(chunk.Choices) == 0 || len(chunk.Choices[0].Delta.ToolCalls) == 0 {
		t.Fatalf("expected tool_calls in chunk")
	}
	tc := chunk.Choices[0].Delta.ToolCalls[0]
	if tc.ID != "call_abc123" {
		t.Errorf("expected id call_abc123, got: %s", tc.ID)
	}
	if tc.Function.Name != "view_file" {
		t.Errorf("expected function name view_file, got: %s", tc.Function.Name)
	}

	// 2. Arguments delta
	outDelta := ProcessCodexEvent(`{
		"type": "response.function_call_arguments.delta",
		"item_id": "item_123",
		"call_id": "call_abc123",
		"delta": "{\"path\":\"file.go\"}"
	}`, state, "chatcmpl-test", 1)

	if len(outDelta) == 0 {
		t.Fatal("expected chunk for arguments.delta")
	}

	// 3. Response completed -> finish_reason should be tool_calls
	outDone := ProcessCodexEvent(`{
		"type": "response.completed",
		"response": {
			"id": "resp_test",
			"usage": {"input_tokens": 10, "output_tokens": 5}
		}
	}`, state, "chatcmpl-test", 1)

	if len(outDone) == 0 {
		t.Fatal("expected chunk for completed")
	}
	var compChunk struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(outDone[0], "data: ")), &compChunk); err != nil {
		t.Fatalf("unmarshal completed chunk: %v", err)
	}
	if compChunk.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("expected finish_reason 'tool_calls', got: %s", compChunk.Choices[0].FinishReason)
	}
}

func TestProcessCodexEvent_ReasoningSummaryDelta(t *testing.T) {
	state := &CodexStreamState{}
	out := ProcessCodexEvent(`{"type":"response.reasoning_summary_text.delta","delta":"Thinking through the logic..."}`, state, "chatcmpl-test", 1)
	if len(out) == 0 {
		t.Fatal("expected chunk for reasoning_summary_text.delta")
	}
	var chunk struct {
		Choices []struct {
			Delta struct {
				ReasoningContent string `json:"reasoning_content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	data := strings.TrimSpace(strings.TrimPrefix(out[0], "data: "))
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		t.Fatalf("unmarshal reasoning chunk: %v", err)
	}
	if len(chunk.Choices) == 0 || chunk.Choices[0].Delta.ReasoningContent != "Thinking through the logic..." {
		t.Errorf("expected reasoning_content, got %+v", chunk)
	}
}

type splitReader struct {
	data      []byte
	chunkSize int
	pos       int
}

func (r *splitReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	toRead := min(min(len(p), r.chunkSize), len(r.data)-r.pos)
	copy(p, r.data[r.pos:r.pos+toRead])
	r.pos += toRead
	return toRead, nil
}

func TestHandleCodexStream_SplitPackets(t *testing.T) {
	rawSSE := "event: response.output_item.added\n" +
		"data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"item_1\",\"type\":\"message\",\"role\":\"assistant\"}}\n\n" +
		"event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello, \"}\n\n" +
		"event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"world!\"}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\"}\n\n" +
		"data: [DONE]\n\n"

	// Feed raw SSE in tiny 10-byte chunks to stress packet boundary splitting
	reader := &splitReader{
		data:      []byte(rawSSE),
		chunkSize: 10,
	}

	rec := httptest.NewRecorder()
	req := &Request{
		IsStream:      true,
		TranslateResp: false,
	}

	err := handleCodexStream(rec, req, reader)
	if err != nil {
		t.Fatalf("handleCodexStream failed: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Hello, ") || !strings.Contains(body, "world!") {
		t.Errorf("expected both deltas, got body: %s", body)
	}
	if !strings.Contains(body, "[DONE]") {
		t.Errorf("expected [DONE], got body: %s", body)
	}
}

func TestHandleCodexStream_UpstreamErrorFailsLoud(t *testing.T) {
	// Upstream FreeTierError must surface as an error, never a 200 with
	// empty content (silent success). Mirrors the live muse-spark-free case.
	rawSSE := "data: {\"type\":\"error\",\"error\":{\"type\":\"FreeTierError\",\"message\":\"Error from provider (Console): OpenCode's free tier can only be used from within OpenCode\"}}\n\n" +
		"data: [DONE]\n\n"
	rec := httptest.NewRecorder()
	req := &Request{IsStream: false, TranslateResp: false}
	err := handleCodexStream(rec, req, strings.NewReader(rawSSE))
	if err == nil {
		t.Fatalf("expected upstream error, got nil (body: %s)", rec.Body.String())
	}
	var ue *proxy.UpstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *proxy.UpstreamError, got %T: %v", err, err)
	}
	if ue.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for FreeTierError, got %d", ue.StatusCode)
	}
	if !strings.Contains(string(ue.Body), "free tier") {
		t.Errorf("expected upstream message preserved, got %s", string(ue.Body))
	}
}

func TestProcessCodexEvent_ErrorRecorded(t *testing.T) {
	state := &CodexStreamState{}
	out := ProcessCodexEvent(`{"type":"error","error":{"type":"FreeTierError","message":"nope"}}`, state, "r", 1)
	if len(out) != 0 {
		t.Errorf("error events must emit no chunks, got %v", out)
	}
	if len(state.UpstreamErr) == 0 {
		t.Fatalf("error event must be recorded on state")
	}
}
