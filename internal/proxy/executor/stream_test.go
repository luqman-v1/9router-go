package executor

import (
	"encoding/json"
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
