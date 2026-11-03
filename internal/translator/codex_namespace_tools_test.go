package translator

import (
	json "encoding/json/v2"
	"testing"
)

func TestResponsesToChatRequest_NamespaceToolsUnpack(t *testing.T) {
	converted := convertedBody(t, `{
		"input": "hi",
		"tools": [
			{
				"type": "namespace",
				"name": "my_namespace",
				"tools": [
					{
						"type": "function",
						"function": {
							"name": "sub_func_1",
							"parameters": {"type": "object", "properties": {"a": {"type": "string"}}}
						}
					},
					{
						"name": "sub_func_2",
						"parameters": {"type": "object", "properties": {"b": {"type": "integer"}}}
					}
				]
			}
		]
	}`)

	tools, ok := converted["tools"].([]any)
	if !ok || len(tools) != 2 {
		t.Fatalf("expected 2 unpacked tools, got %v", converted["tools"])
	}
}

func TestResponsesToChatRequest_TolerateFunctionsPrefix(t *testing.T) {
	result := convertRequest(t, `{
		"input": [
			{
				"type": "function_call",
				"call_id": "call_1",
				"name": "functions.my_custom_tool",
				"arguments": "{}"
			}
		]
	}`)

	var out map[string]any
	if err := json.Unmarshal(result.Body, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	messages, _ := out["messages"].([]any)
	if len(messages) == 0 {
		t.Fatalf("no messages")
	}
	msg := messages[0].(map[string]any)
	tcs, _ := msg["tool_calls"].([]any)
	if len(tcs) == 0 {
		t.Fatalf("expected tool calls")
	}
	tc := tcs[0].(map[string]any)
	fn := tc["function"].(map[string]any)
	if fn["name"] != "my_custom_tool" {
		t.Errorf("tool call name = %v, want my_custom_tool (functions. prefix stripped)", fn["name"])
	}
}

func TestCleanGeminiSchema_EncryptedConstraint(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"secret": map[string]any{
				"type":      "string",
				"encrypted": true,
			},
		},
	}
	cleanGeminiSchema(schema)
	props := schema["properties"].(map[string]any)
	secret := props["secret"].(map[string]any)
	if _, ok := secret["encrypted"]; ok {
		t.Errorf("expected 'encrypted' keyword to be cleaned from schema, got %v", secret)
	}
}
