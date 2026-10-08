package translator

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

// kiroToolSpecs digs the tool catalogue out of the final user turn of a Kiro
// conversationState payload.
func kiroToolSpecs(t *testing.T, payload map[string]any) []any {
	t.Helper()
	cs, _ := payload["conversationState"].(map[string]any)
	cur, _ := cs["currentMessage"].(map[string]any)
	uim, _ := cur["userInputMessage"].(map[string]any)
	ctx, _ := uim["userInputMessageContext"].(map[string]any)
	specs, _ := ctx["tools"].([]any)
	return specs
}

// Kiro has no top-level `tools` array — the catalogue must land on the last
// user turn as userInputMessageContext.tools, or the model answers with a
// pseudo tool call written as text instead of a real toolUseEvent.
func TestOpenAIToKiro_AttachesToolSpecsToLastUserTurn(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4.5",
		"messages": [
			{"role": "system", "content": "be brief"},
			{"role": "user", "content": "weather in Jakarta?"}
		],
		"tools": [
			{"type": "function", "function": {
				"name": "get_weather",
				"description": "Get current weather",
				"parameters": {
					"type": "object",
					"properties": {"city": {"type": "string"}, "unit": {"type": "string"}},
					"required": ["city"],
					"additionalProperties": false
				}
			}}
		]
	}`)

	out, err := OpenAIToKiro(body, KiroTranslateOptions{Model: "claude-sonnet-4.5"})
	if err != nil {
		t.Fatalf("OpenAIToKiro: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("unmarshal kiro payload: %v", err)
	}

	specs := kiroToolSpecs(t, payload)
	if len(specs) != 1 {
		t.Fatalf("expected 1 tool spec on the last user turn, got %d", len(specs))
	}
	entry, _ := specs[0].(map[string]any)
	spec, _ := entry["toolSpecification"].(map[string]any)
	if spec["name"] != "get_weather" {
		t.Errorf("tool name = %v, want get_weather", spec["name"])
	}
	if spec["description"] != "Get current weather" {
		t.Errorf("tool description = %v", spec["description"])
	}
	inputSchema, _ := spec["inputSchema"].(map[string]any)
	schema, _ := inputSchema["json"].(map[string]any)
	if schema["type"] != "object" {
		t.Errorf("schema type = %v, want object", schema["type"])
	}
	props, _ := schema["properties"].(map[string]any)
	if _, ok := props["city"]; !ok {
		t.Errorf("schema lost the city property: %v", schema)
	}
	if _, leaked := schema["additionalProperties"]; leaked {
		t.Errorf("additionalProperties must be stripped: %v", schema)
	}
	required, _ := schema["required"].([]any)
	if len(required) != 1 || required[0] != "city" {
		t.Errorf("required = %v, want [city]", required)
	}
}

// A tool call followed by a tool result must still round-trip: the catalogue
// and the toolResults share the same context object on the last turn.
func TestOpenAIToKiro_ToolResultsStillTravel(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4.5",
		"messages": [
			{"role": "user", "content": "weather?"},
			{"role": "assistant", "content": "", "tool_calls": [
				{"id": "call_1", "type": "function", "function": {"name": "get_weather", "arguments": "{\"city\":\"Jakarta\"}"}}
			]},
			{"role": "tool", "tool_call_id": "call_1", "content": "31C, humid"}
		],
		"tools": [
			{"type": "function", "function": {"name": "get_weather", "parameters": {"type": "object", "properties": {"city": {"type": "string"}}}}}
		]
	}`)

	out, err := OpenAIToKiro(body, KiroTranslateOptions{Model: "claude-sonnet-4.5"})
	if err != nil {
		t.Fatalf("OpenAIToKiro: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if specs := kiroToolSpecs(t, payload); len(specs) != 1 {
		t.Errorf("tool catalogue missing from the last user turn: %d specs", len(specs))
	}
	cs, _ := payload["conversationState"].(map[string]any)
	cur, _ := cs["currentMessage"].(map[string]any)
	uim, _ := cur["userInputMessage"].(map[string]any)
	ctx, _ := uim["userInputMessageContext"].(map[string]any)
	results, _ := ctx["toolResults"].([]any)
	if len(results) == 0 {
		t.Error("tool results were dropped when the catalogue was attached")
	}
}

// A body without tools must not grow an empty catalogue.
func TestOpenAIToKiro_NoToolsNoContext(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4.5","messages":[{"role":"user","content":"hi"}]}`)
	out, err := OpenAIToKiro(body, KiroTranslateOptions{Model: "claude-sonnet-4.5"})
	if err != nil {
		t.Fatalf("OpenAIToKiro: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if specs := kiroToolSpecs(t, payload); specs != nil {
		t.Errorf("expected no tool catalogue, got %v", specs)
	}
}

func TestKiroNormalizeRootSchema(t *testing.T) {
	schema := map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"a": map[string]any{"type": "string"}},
		"required":             []any{"a", "ghost", "a"},
		"additionalProperties": false,
	}
	got := kiroNormalizeRootSchema(schema)

	if got["type"] != "object" {
		t.Errorf("type = %v", got["type"])
	}
	if _, ok := got["additionalProperties"]; ok {
		t.Error("additionalProperties must be stripped")
	}
	req, _ := got["required"].([]any)
	if len(req) != 1 || req[0] != "a" {
		t.Errorf("required = %v, want only the declared, de-duplicated [a]", req)
	}

	empty := kiroNormalizeRootSchema(map[string]any{"required": []any{}})
	props, _ := empty["properties"].(map[string]any)
	if props == nil {
		t.Errorf("expected properties to be defaulted, got %v", empty)
	}
	if _, ok := empty["required"]; ok {
		t.Errorf("empty required must be dropped, got %v", empty)
	}
}

func TestKiroUniqueToolName(t *testing.T) {
	used := map[string]bool{}
	if got := kiroUniqueToolName("weird name!", 0, used); got != "weird_name" {
		t.Errorf("sanitized name = %q, want weird_name", got)
	}
	if got := kiroUniqueToolName("weird name!", 0, used); got != "weird_name_2" {
		t.Errorf("collision suffix = %q, want weird_name_2", got)
	}
	if got := kiroUniqueToolName("__", 3, used); got != "tool_4" {
		t.Errorf("empty name fallback = %q, want tool_4", got)
	}
	if got := kiroUniqueToolName(strings.Repeat("a", 80), 0, used); len(got) != kiroToolNameMaxLength {
		t.Errorf("long name not truncated: %d chars", len(got))
	}
}
