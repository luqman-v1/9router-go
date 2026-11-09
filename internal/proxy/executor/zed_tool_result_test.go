package executor

import (
	"encoding/json"
	"testing"
)

// Zed re-serializes the Anthropic body through a strict Rust struct and
// answers "missing field `is_error`" when a tool_result omits it — a body
// Anthropic itself accepts. Every tool_result the converter emits therefore
// carries the field, defaulting to false.
func TestToolResultCarriesIsError(t *testing.T) {
	// Two exchanges so the converter has to pair tool_use with tool_result
	// rather than copying a single message verbatim.
	body := []byte(`{
		"model": "claude-haiku-5-5",
		"messages": [
			{"role": "user", "content": "list the files"},
			{"role": "assistant", "content": "", "tool_calls": [
				{"id": "call_1", "type": "function", "function": {"name": "ls", "arguments": "{}"}},
				{"id": "call_2", "type": "function", "function": {"name": "pwd", "arguments": "{}"}}
			]},
			{"role": "tool", "tool_call_id": "call_1", "content": "a.txt\nb.txt"},
			{"role": "tool", "tool_call_id": "call_2", "content": "boom", "is_error": true}
		],
		"tools": [{"type": "function", "function": {"name": "ls", "description": "list", "parameters": {"type": "object"}}}]
	}`)

	out := EnsureClaudeMessagesFor(body, "anthropic", "claude-haiku-5-5")
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("converted body is not JSON: %v", err)
	}

	msgs, _ := parsed["messages"].([]any)
	seen := 0
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		blocks, _ := mm["content"].([]any)
		for _, b := range blocks {
			bb, _ := b.(map[string]any)
			if bb["type"] != "tool_result" {
				continue
			}
			seen++
			val, ok := bb["is_error"]
			if !ok {
				t.Errorf("tool_result %v has no is_error field", bb["tool_use_id"])
				continue
			}
			bVal, isBool := val.(bool)
			if !isBool {
				t.Errorf("is_error is %T, want bool", val)
			}
			if bb["tool_use_id"] == "call_1" && bVal != false {
				t.Errorf("tool_result call_1 is_error = %v, want false", bVal)
			}
			if bb["tool_use_id"] == "call_2" && bVal != true {
				t.Errorf("tool_result call_2 is_error = %v, want true", bVal)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("tool_result blocks = %d, want 2", seen)
	}
}

func TestZedClaudeProviderRequest_AlwaysEmitsIsError(t *testing.T) {
	// Replay tests ensuring Zed completions wire receives the expected shape.
	tests := []struct {
		name          string
		input         string
		expectedError map[string]bool
	}{
		{
			name: "mixed tool results with error status and json error",
			input: `{
				"messages": [
					{"role": "user", "content": "run actions"},
					{"role": "assistant", "content": "", "tool_calls": [
						{"id": "call_ok", "type": "function", "function": {"name": "read", "arguments": "{}"}},
						{"id": "call_err_status", "type": "function", "function": {"name": "exec", "arguments": "{}"}},
						{"id": "call_json_err", "type": "function", "function": {"name": "query", "arguments": "{}"}}
					]},
					{"role": "tool", "tool_call_id": "call_ok", "content": "file contents"},
					{"role": "tool", "tool_call_id": "call_err_status", "status": "error", "content": "command failed: exit status 1"},
					{"role": "tool", "tool_call_id": "call_json_err", "content": "{\"error\":\"not found\"}"}
				]
			}`,
			expectedError: map[string]bool{
				"call_ok":         false,
				"call_err_status": true,
				"call_json_err":   true,
			},
		},
		{
			name: "pre-existing Claude body with tool_result missing is_error",
			input: `{
				"anthropic_version": "2023-06-01",
				"messages": [
					{"role": "user", "content": "run action"},
					{"role": "assistant", "content": [
						{"type": "tool_use", "id": "t1", "name": "ls", "input": {}}
					]},
					{"role": "user", "content": [
						{"type": "tool_result", "tool_use_id": "t1", "content": "a.txt"}
					]}
				]
			}`,
			expectedError: map[string]bool{
				"t1": false,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := zedClaudeProviderRequest([]byte(tt.input), "claude-haiku-5-5")
			if err != nil {
				t.Fatalf("zedClaudeProviderRequest failed: %v", err)
			}
			msgs, ok := req["messages"].([]any)
			if !ok {
				t.Fatalf("messages is not a slice: %T", req["messages"])
			}
			found := 0
			for _, m := range msgs {
				mm, ok := m.(map[string]any)
				if !ok {
					continue
				}
				blocks, ok := mm["content"].([]any)
				if !ok {
					continue
				}
				for _, b := range blocks {
					bm, ok := b.(map[string]any)
					if !ok || bm["type"] != "tool_result" {
						continue
					}
					id, _ := bm["tool_use_id"].(string)
					wantErr, expected := tt.expectedError[id]
					if !expected {
						t.Errorf("unexpected tool_result id %q", id)
						continue
					}
					found++
					val, ok := bm["is_error"]
					if !ok {
						t.Errorf("tool_result %q missing is_error field", id)
						continue
					}
					isErr, isBool := val.(bool)
					if !isBool {
						t.Errorf("tool_result %q is_error is %T, want bool", id, val)
						continue
					}
					if isErr != wantErr {
						t.Errorf("tool_result %q is_error = %v, want %v", id, isErr, wantErr)
					}
				}
			}
			if found != len(tt.expectedError) {
				t.Errorf("found %d tool_results, want %d", found, len(tt.expectedError))
			}
		})
	}
}
