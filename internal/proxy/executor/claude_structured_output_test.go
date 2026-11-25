package executor

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestEnsureClaudeMessages_StructuredOutput covers the request-side carry of an
// OpenAI response_format onto the Claude Messages payload built by
// EnsureClaudeMessages: json_schema without tools becomes the forced
// structured-output tool with a pinned tool_choice, the other shapes fall back
// to a system-prompt instruction, and the OpenAI-only field is always removed
// so the strict Claude API does not 400 on it.
func TestEnsureClaudeMessages_StructuredOutput(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		wantForced    bool
		wantSystemHas string
	}{
		{
			name:       "json_schema without tools becomes forced tool",
			body:       `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_schema","json_schema":{"name":"Answer","schema":{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"]}}}}`,
			wantForced: true,
		},
		{
			name:          "json_object becomes system instruction",
			body:          `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}],"response_format":"json_object"}`,
			wantSystemHas: "single valid JSON object",
		},
		{
			name:          "client tools keep the system-instruction fallback",
			body:          `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object"}}}],"response_format":{"type":"json_schema","json_schema":{"name":"Answer","schema":{"type":"object","properties":{"answer":{"type":"string"}}}}}}`,
			wantSystemHas: "JSON schema",
		},
		{
			name: "no response_format passes through",
			body: `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := EnsureClaudeMessages([]byte(tt.body), "claude-sonnet-4-5")
			var reqMap map[string]any
			if err := json.Unmarshal(out, &reqMap); err != nil {
				t.Fatalf("unmarshal output: %v (out=%s)", err, out)
			}
			if _, has := reqMap["response_format"]; has {
				t.Errorf("response_format must be removed, got %v", reqMap["response_format"])
			}
			if tt.wantForced {
				tools, ok := reqMap["tools"].([]any)
				if !ok || len(tools) != 1 {
					t.Fatalf("expected one forced tool, got %v", reqMap["tools"])
				}
				tool, _ := tools[0].(map[string]any)
				if tool["name"] != "9router_structured_output" {
					t.Errorf("forced tool name = %v, want 9router_structured_output", tool["name"])
				}
				if _, ok := tool["input_schema"]; !ok {
					t.Errorf("forced tool missing input_schema: %v", tool)
				}
				tc, ok := reqMap["tool_choice"].(map[string]any)
				if !ok || tc["type"] != "tool" || tc["name"] != "9router_structured_output" {
					t.Errorf("tool_choice = %v, want pinned to the forced tool", reqMap["tool_choice"])
				}
				return
			}
			if tt.wantSystemHas != "" {
				sys, _ := reqMap["system"].(string)
				if !strings.Contains(sys, tt.wantSystemHas) {
					t.Errorf("system = %q, want it to contain %q", sys, tt.wantSystemHas)
				}
			}
		})
	}
}

// TestEnsureClaudeMessages_StructuredOutput_ThinkingOn pins the fallback: a
// pinned tool_choice is rejected while extended thinking is enabled, so the
// schema must ride in the system prompt instead.
func TestEnsureClaudeMessages_StructuredOutput_ThinkingOn(t *testing.T) {
	body := `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":2048},"response_format":{"type":"json_schema","json_schema":{"name":"Answer","schema":{"type":"object","properties":{"answer":{"type":"string"}}}}}}`
	out := EnsureClaudeMessages([]byte(body), "claude-sonnet-4-5")
	var reqMap map[string]any
	if err := json.Unmarshal(out, &reqMap); err != nil {
		t.Fatalf("unmarshal output: %v (out=%s)", err, out)
	}
	if _, has := reqMap["tool_choice"]; has {
		t.Errorf("thinking on must not pin a tool_choice, got %v", reqMap["tool_choice"])
	}
	sys, _ := reqMap["system"].(string)
	if !strings.Contains(sys, "JSON schema") {
		t.Errorf("system = %q, want the schema instruction", sys)
	}
}
