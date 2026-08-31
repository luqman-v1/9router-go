package executor

import (
	json "encoding/json/v2"
	"strings"
	"testing"
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

// response.function_call_arguments.done must carry the same id/index as the
// incremental deltas so the tool name attaches to the right tool call.
func TestProcessCodexEvent_DoneCarriesToolCallID(t *testing.T) {
	state := &CodexStreamState{}
	// First fragment assigns index 0 to call_abc.
	ProcessCodexEvent(`{"type":"response.function_call_arguments.delta","call_id":"call_abc","delta":"{}","name":"Read"}`, state, "chatcmpl-x", 1)
	out := ProcessCodexEvent(`{"type":"response.function_call_arguments.done","call_id":"call_abc","name":"Read","arguments":"{\"path\":\"/tmp\"}"}`, state, "chatcmpl-x", 1)
	if len(out) == 0 {
		t.Fatal("expected done chunk")
	}
	id, idx := parseToolCalls(t, out[0])
	if id != "call_abc" || idx != 0 {
		t.Errorf("done chunk: got id=%q idx=%d, want call_abc/0", id, idx)
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
	out := ProcessCommandcodeEvent(map[string]interface{}{
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
