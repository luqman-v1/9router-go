package executor

import (
	json "encoding/json/v2"
	"strings"
	"testing"

	"9router/proxy/internal/translator"
)

// --- OpenAI -> Claude (convertOpenAIToolsToClaude) ---

// claudeToolStrict converts a Chat Completions tool list and returns the single
// resulting Claude tool. A client strict mode must reach the Claude tool as a
// real instruction; an unset one must leave no key behind, because a synthesised
// false would change how the upstream validates arguments.
func claudeToolStrict(t *testing.T, function string) map[string]any {
	t.Helper()
	var tools []any
	if err := json.Unmarshal([]byte(`[{"type":"function","function":`+function+`}]`), &tools); err != nil {
		t.Fatalf("decode tools: %v", err)
	}
	out := convertOpenAIToolsToClaude(tools)
	if len(out) != 1 {
		t.Fatalf("got %d tools, want 1", len(out))
	}
	m, ok := out[0].(map[string]any)
	if !ok {
		t.Fatalf("tool is %T, want map[string]any", out[0])
	}
	return m
}

func TestConvertOpenAIToolsToClaudeStrict(t *testing.T) {
	tests := []struct {
		name     string
		function string
		want     any // nil means "key must be absent"
	}{
		{"strict true", `{"name":"probe","strict":true,"parameters":{"type":"object","properties":{}}}`, true},
		{"strict false", `{"name":"probe","strict":false,"parameters":{"type":"object","properties":{}}}`, false},
		{"strict absent", `{"name":"probe","parameters":{"type":"object","properties":{}}}`, nil},
		{"strict non-boolean is not an instruction", `{"name":"probe","strict":"yes","parameters":{"type":"object","properties":{}}}`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := claudeToolStrict(t, tt.function)
			if m["name"] != "probe" {
				t.Errorf("name = %v, want probe", m["name"])
			}
			if _, ok := m["input_schema"]; !ok {
				t.Error("input_schema missing")
			}
			switch tt.want {
			case nil:
				if _, present := m["strict"]; present {
					t.Errorf("strict must stay absent, got %v", m["strict"])
				}
			default:
				if _, present := m["strict"]; !present {
					t.Fatalf("strict missing, want %v", tt.want)
				}
				if m["strict"] != tt.want {
					t.Errorf("strict = %v, want %v", m["strict"], tt.want)
				}
			}
		})
	}
}

// --- Codex Responses tool build ---

// codexToolStrict runs a Chat Completions body through buildResponsesBody and
// returns the forwarded Responses tool.
func codexToolStrict(t *testing.T, tool string) map[string]any {
	t.Helper()
	req := buildCodexBody(t, `{"model":"gpt-5.6-terra","messages":[{"role":"user","content":"hi"}],"tools":[`+tool+`]}`)
	tools := toolList(req)
	if len(tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(tools))
	}
	m, ok := tools[0].(map[string]any)
	if !ok {
		t.Fatalf("tool is %T, want map[string]any", tools[0])
	}
	return m
}

func TestBuildResponsesBodyToolStrict(t *testing.T) {
	tests := []struct {
		name string
		tool string
		want any // nil means "key must be absent"
	}{
		{"nested strict true", `{"type":"function","function":{"name":"probe","strict":true,"parameters":{"type":"object","properties":{}}}}`, true},
		{"nested strict false", `{"type":"function","function":{"name":"probe","strict":false,"parameters":{"type":"object","properties":{}}}}`, false},
		{"nested strict absent", `{"type":"function","function":{"name":"probe","parameters":{"type":"object","properties":{}}}}`, false},
		{"flat strict true", `{"type":"function","name":"probe","strict":true,"parameters":{"type":"object","properties":{}}}`, true},
		{"flat strict false", `{"type":"function","name":"probe","strict":false,"parameters":{"type":"object","properties":{}}}`, false},
		{"flat strict absent", `{"type":"function","name":"probe","parameters":{"type":"object","properties":{}}}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := codexToolStrict(t, tt.tool)
			if m["type"] != "function" {
				t.Errorf("type = %v, want function", m["type"])
			}
			if m["name"] != "probe" {
				t.Errorf("name = %v, want probe", m["name"])
			}
			if _, ok := m["parameters"]; !ok {
				t.Error("parameters missing")
			}
			switch tt.want {
			case nil:
				if _, present := m["strict"]; present {
					t.Errorf("strict must stay absent, got %v", m["strict"])
				}
			default:
				strict, ok := m["strict"].(bool)
				if !ok {
					t.Fatalf("strict missing, want %v", tt.want)
				}
				if strict != tt.want {
					t.Errorf("strict = %v, want %v", strict, tt.want)
				}
			}
		})
	}
}

// An already-Responses body skips the Chat Completions rebuild, so its tools
// pass through untouched and keep whatever strict the client sent.
func TestBuildResponsesBodyPassthroughKeepsStrict(t *testing.T) {
	tests := []struct {
		name string
		tool string
		want any // nil means "key must be absent"
	}{
		{"strict true", `{"type":"function","name":"probe","strict":true,"parameters":{"type":"object","properties":{}}}`, true},
		{"strict false", `{"type":"function","name":"probe","strict":false,"parameters":{"type":"object","properties":{}}}`, false},
		{"strict absent", `{"type":"function","name":"probe","parameters":{"type":"object","properties":{}}}`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := buildCodexBody(t, `{"model":"gpt-5.6-terra","input":[{"type":"message","role":"user","content":"hi"}],"tools":[`+tt.tool+`]}`)
			tools := toolList(req)
			if len(tools) != 1 {
				t.Fatalf("got %d tools, want 1", len(tools))
			}
			m, ok := tools[0].(map[string]any)
			if !ok {
				t.Fatalf("tool is %T, want map[string]any", tools[0])
			}
			switch tt.want {
			case nil:
				if _, present := m["strict"]; present {
					t.Errorf("strict must stay absent, got %v", m["strict"])
				}
			default:
				if m["strict"] != tt.want {
					t.Errorf("strict = %v, want %v", m["strict"], tt.want)
				}
			}
		})
	}
}

// Responses normalizes omitted strict into a stricter schema. Chat tools must
// retain their optional fields rather than forcing invented enum values.
func TestBuildResponsesBodyPreservesOptionalToolArguments(t *testing.T) {
	const parameters = `{"type":"object","properties":{"prompt":{"type":"string"},"isolation":{"type":"string","enum":["worktree"]},"effort":{"type":"string","enum":["low","medium","high"]}},"required":["prompt"]}`
	tests := []struct {
		name, tool string
		wantStrict bool
	}{
		{"nested omitted", `{"type":"function","function":{"name":"probe","parameters":` + parameters + `}}`, false},
		{"flat omitted", `{"type":"function","name":"probe","parameters":` + parameters + `}`, false},
		{"explicit false", `{"type":"function","function":{"name":"probe","strict":false,"parameters":` + parameters + `}}`, false},
		{"explicit true", `{"type":"function","function":{"name":"probe","strict":true,"parameters":` + parameters + `}}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := codexToolStrict(t, tt.tool)
			strict, present := tool["strict"].(bool)
			if !present || strict != tt.wantStrict {
				t.Errorf("forwarded strict = %v, present = %v; want %v", tool["strict"], present, tt.wantStrict)
			}
			encoded, err := json.Marshal(tool["parameters"])
			if err != nil {
				t.Fatalf("marshal parameters: %v", err)
			}
			var schema struct {
				Required []string `json:"required"`
			}
			if err := json.Unmarshal(encoded, &schema); err != nil {
				t.Fatalf("decode schema: %v", err)
			}
			if len(schema.Required) != 1 || schema.Required[0] != "prompt" {
				t.Errorf("required = %v; optional isolation and effort must remain optional", schema.Required)
			}
		})
	}
}

func TestClaudeOptionalToolArgumentsRemainOptionalInResponses(t *testing.T) {
	body := []byte(`{"model":"gpt-6.1-sol","max_tokens":128,"messages":[{"role":"user","content":"Use workspace chung; omit isolation."}],"tools":[{"name":"probe","description":"Do not invent optional arguments","input_schema":{"type":"object","properties":{"prompt":{"type":"string"},"isolation":{"type":"string","enum":["worktree"]}},"required":["prompt"]}}]}`)
	chat, err := translator.TranslateClaudeToOpenAI(body)
	if err != nil {
		t.Fatalf("translate Claude request: %v", err)
	}
	responses, _, err := buildResponsesBody(chat)
	if err != nil {
		t.Fatalf("build Responses request: %v", err)
	}
	var request map[string]any
	if err := json.Unmarshal(responses, &request); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	tools := toolList(request)
	// Sol uses Responses Lite, whose declarations live in an input prefix.
	if len(tools) == 0 {
		items, _ := request["input"].([]any)
		for _, raw := range items {
			item, _ := raw.(map[string]any)
			if item["type"] == "additional_tools" {
				tools, _ = item["tools"].([]any)
				break
			}
		}
	}
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(tools))
	}
	tool := tools[0].(map[string]any)
	strict, exists := tool["strict"].(bool)
	if !exists || strict {
		t.Fatalf("strict = %v, exists = %v; want explicit false", tool["strict"], exists)
	}
	schema := tool["parameters"].(map[string]any)
	required := schema["required"].([]any)
	if len(required) != 1 || required[0] != "prompt" {
		t.Errorf("required = %v; isolation must remain optional", required)
	}
}

func TestBuildResponsesBody_StripsAgentIsolationFromSchemaAndHistory(t *testing.T) {
	chatBody := []byte(`{
		"model": "gpt-6.1-sol",
		"messages": [
			{"role": "user", "content": "run task"},
			{"role": "assistant", "content": null, "tool_calls": [
				{"id": "call_1", "type": "function", "function": {"name": "Agent", "arguments": "{\"description\":\"task\",\"isolation\":\"worktree\",\"prompt\":\"do it\"}"}}
			]},
			{"role": "tool", "tool_call_id": "call_1", "content": "done"}
		],
		"tools": [
			{"type": "function", "function": {
				"name": "Agent",
				"parameters": {
					"type": "object",
					"properties": {
						"description": {"type": "string"},
						"prompt": {"type": "string"},
						"isolation": {"type": "string", "enum": ["worktree", "remote"]}
					},
					"required": ["description", "prompt"]
				}
			}}
		]
	}`)

	respBody, _, err := buildResponsesBody(chatBody)
	if err != nil {
		t.Fatalf("buildResponsesBody failed: %v", err)
	}

	var respReq map[string]any
	if err := json.Unmarshal(respBody, &respReq); err != nil {
		t.Fatalf("unmarshal responses failed: %v", err)
	}

	// 1. Tool declaration must have isolation stripped
	tools := toolList(respReq)
	if len(tools) == 0 {
		items, _ := respReq["input"].([]any)
		for _, raw := range items {
			item, _ := raw.(map[string]any)
			if item["type"] == "additional_tools" {
				tools, _ = item["tools"].([]any)
				break
			}
		}
	}
	if len(tools) != 1 {
		t.Fatalf("tools count = %d, want 1", len(tools))
	}
	toolMap := tools[0].(map[string]any)
	paramsMap := toolMap["parameters"].(map[string]any)
	props := paramsMap["properties"].(map[string]any)
	if _, has := props["isolation"]; has {
		t.Errorf("Agent tool schema still has isolation property: %v", props)
	}

	// 2. History tool call arguments must have isolation stripped
	inputItems, _ := respReq["input"].([]any)
	foundCall := false
	for _, raw := range inputItems {
		item, _ := raw.(map[string]any)
		if item["type"] == "function_call" && item["name"] == "Agent" {
			foundCall = true
			args := item["arguments"].(string)
			if strings.Contains(args, "worktree") || strings.Contains(args, "isolation") {
				t.Errorf("history Agent tool call arguments still contain isolation: %s", args)
			}
		}
	}
	if !foundCall {
		t.Error("expected function_call for Agent in input items")
	}
}
