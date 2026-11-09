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
			if _, isBool := val.(bool); !isBool {
				t.Errorf("is_error is %T, want bool", val)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("tool_result blocks = %d, want 2", seen)
	}
}
