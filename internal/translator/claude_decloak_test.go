package translator

import (
	json "encoding/json/v2"
	"testing"
)

func TestDecloakStreamChunk(t *testing.T) {
	toolNameMap := map[string]string{
		"run_code_ide":  "run_code",
		"read_file_ide": "read_file",
	}

	// 1. Tool use start event should decloak name
	chunk := []byte(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_01xyz","name":"run_code_ide","input":{}}}`)
	out := DecloakStreamChunk(chunk, toolNameMap)

	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}

	block := parsed["content_block"].(map[string]any)
	if block["name"] != "run_code" {
		t.Errorf("expected decloaked name 'run_code', got %v", block["name"])
	}

	// 2. Unknown tool names (e.g. decoy tools) pass through unchanged
	decoyChunk := []byte(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_02","name":"Task","input":{}}}`)
	outDecoy := DecloakStreamChunk(decoyChunk, toolNameMap)
	var parsedDecoy map[string]any
	json.Unmarshal(outDecoy, &parsedDecoy)
	if parsedDecoy["content_block"].(map[string]any)["name"] != "Task" {
		t.Errorf("expected decoy tool name 'Task' untouched, got %v", parsedDecoy["content_block"].(map[string]any)["name"])
	}

	// 3. Text delta events pass through untouched
	textChunk := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`)
	outText := DecloakStreamChunk(textChunk, toolNameMap)
	if string(outText) != string(textChunk) {
		t.Errorf("expected text chunk untouched, got %s", string(outText))
	}

	// 4. Empty map or nil returns input
	outEmpty := DecloakStreamChunk(chunk, nil)
	if string(outEmpty) != string(chunk) {
		t.Errorf("expected chunk unchanged when toolNameMap is nil")
	}
}

func TestDecloakClaudeStreamEvent(t *testing.T) {
	toolNameMap := map[string]string{
		"bash_cmd_ide": "bash_cmd",
	}

	event := map[string]any{
		"type":  "content_block_start",
		"index": 0,
		"content_block": map[string]any{
			"type":  "tool_use",
			"id":    "call_1",
			"name":  "bash_cmd_ide",
			"input": map[string]any{},
		},
	}

	out := DecloakClaudeStreamEvent(event, toolNameMap)
	block := out["content_block"].(map[string]any)
	if block["name"] != "bash_cmd" {
		t.Errorf("expected bash_cmd, got %v", block["name"])
	}

	// Input map was not mutated
	origBlock := event["content_block"].(map[string]any)
	if origBlock["name"] != "bash_cmd_ide" {
		t.Errorf("expected original event untouched, got %v", origBlock["name"])
	}
}
