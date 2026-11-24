package translator

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCarryClaudeStructuredOutput(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		wantForced    bool // forced tool + pinned tool_choice
		wantSystemHas string
	}{
		{
			name:       "json_schema without tools becomes forced tool",
			body:       `{"model":"m","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_schema","json_schema":{"name":"Answer","schema":{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"]}}}}`,
			wantForced: true,
		},
		{
			name:          "json_object without tools becomes system instruction",
			body:          `{"model":"m","messages":[{"role":"user","content":"hi"}],"response_format":"json_object"}`,
			wantSystemHas: "single valid JSON object",
		},
		{
			name:          "json_schema with client tools falls back to system instruction",
			body:          `{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object"}}}],"response_format":{"type":"json_schema","json_schema":{"name":"Answer","schema":{"type":"object","properties":{"answer":{"type":"string"}}}}}}`,
			wantSystemHas: "JSON schema",
		},
		{
			name:          "json_schema with thinking on falls back to system instruction",
			body:          `{"model":"m","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":1024},"response_format":{"type":"json_schema","json_schema":{"name":"Answer","schema":{"type":"object","properties":{"answer":{"type":"string"}}}}}}`,
			wantSystemHas: "JSON schema",
		},
		{
			name:          "reserved tool name declared by client falls back to system instruction",
			body:          `{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"9router_structured_output","input_schema":{"type":"object"}}],"response_format":{"type":"json_schema","json_schema":{"name":"Answer","schema":{"type":"object","properties":{"answer":{"type":"string"}}}}}}`,
			wantSystemHas: "JSON schema",
		},
		{
			name: "unparseable response_format is dropped",
			body: `{"model":"m","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"weird_format"}}`,
		},
		{
			name: "no response_format leaves the body alone",
			body: `{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var reqMap map[string]any
			if err := json.Unmarshal([]byte(tt.body), &reqMap); err != nil {
				t.Fatalf("unmarshal body: %v", err)
			}
			CarryClaudeStructuredOutput(reqMap)

			if _, has := reqMap["response_format"]; has {
				t.Errorf("response_format must be removed, got %v", reqMap["response_format"])
			}
			if tt.wantForced {
				tools, ok := reqMap["tools"].([]any)
				if !ok || len(tools) != 1 {
					t.Fatalf("expected one forced tool, got %v", reqMap["tools"])
				}
				tool, ok := tools[0].(map[string]any)
				if !ok {
					t.Fatalf("tool is not an object: %v", tools[0])
				}
				if tool["name"] != StructuredOutputTool {
					t.Errorf("forced tool name = %v, want %q", tool["name"], StructuredOutputTool)
				}
				if _, ok := tool["input_schema"]; !ok {
					t.Errorf("forced tool missing input_schema: %v", tool)
				}
				tc, ok := reqMap["tool_choice"].(map[string]any)
				if !ok || tc["type"] != "tool" || tc["name"] != StructuredOutputTool {
					t.Errorf("tool_choice = %v, want pinned to %q", reqMap["tool_choice"], StructuredOutputTool)
				}
				return
			}
			if _, has := reqMap["tool_choice"]; has {
				t.Errorf("no forced tool expected, but tool_choice = %v", reqMap["tool_choice"])
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

// TestCarryClaudeStructuredOutput_ExistingSystem verifies the instruction is
// appended to a client-provided system prompt in every shape (string, block
// array) instead of replacing it.
func TestCarryClaudeStructuredOutput_ExistingSystem(t *testing.T) {
	t.Run("string system", func(t *testing.T) {
		reqMap := map[string]any{
			"system":          "be brief",
			"messages":        []any{map[string]any{"role": "user", "content": "hi"}},
			"response_format": map[string]any{"type": "json_object"},
		}
		CarryClaudeStructuredOutput(reqMap)
		sys, _ := reqMap["system"].(string)
		if !strings.Contains(sys, "be brief") || !strings.Contains(sys, "single valid JSON object") {
			t.Errorf("system = %q, want both the client text and the instruction", sys)
		}
	})
	t.Run("block array system", func(t *testing.T) {
		reqMap := map[string]any{
			"system":          []any{map[string]any{"type": "text", "text": "be brief"}},
			"messages":        []any{map[string]any{"role": "user", "content": "hi"}},
			"response_format": map[string]any{"type": "json_object"},
		}
		CarryClaudeStructuredOutput(reqMap)
		blocks, ok := reqMap["system"].([]any)
		if !ok || len(blocks) != 2 {
			t.Fatalf("system = %v, want two blocks", reqMap["system"])
		}
	})
}

func TestTranslateClaudeResponseToOpenAI_StructuredOutputUnwrap(t *testing.T) {
	t.Run("forced tool becomes content with finish_reason stop", func(t *testing.T) {
		claudeJSON := []byte(`{
			"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5",
			"content":[{"type":"tool_use","id":"toolu_1","name":"9router_structured_output","input":{"answer":"42"}}],
			"stop_reason":"tool_use",
			"usage":{"input_tokens":10,"output_tokens":5}
		}`)
		out, err := TranslateClaudeResponseToOpenAI(claudeJSON)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		var resp struct {
			Choices []struct {
				Message struct {
					Content   string           `json:"content"`
					ToolCalls []map[string]any `json:"tool_calls"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(out, &resp); err != nil {
			t.Fatalf("unmarshal output: %v (out=%s)", err, out)
		}
		if len(resp.Choices) != 1 {
			t.Fatalf("expected one choice, got %d", len(resp.Choices))
		}
		if resp.Choices[0].Message.Content != `{"answer":"42"}` {
			t.Errorf("content = %q, want the tool input JSON", resp.Choices[0].Message.Content)
		}
		if len(resp.Choices[0].Message.ToolCalls) != 0 {
			t.Errorf("tool_calls = %v, want none", resp.Choices[0].Message.ToolCalls)
		}
		if resp.Choices[0].FinishReason != "stop" {
			t.Errorf("finish_reason = %q, want stop", resp.Choices[0].FinishReason)
		}
	})
	t.Run("client tool is not unwrapped", func(t *testing.T) {
		claudeJSON := []byte(`{
			"id":"msg_2","type":"message","role":"assistant","model":"claude-sonnet-4-5",
			"content":[{"type":"tool_use","id":"toolu_2","name":"get_weather","input":{"city":"Yogyakarta"}}],
			"stop_reason":"tool_use",
			"usage":{"input_tokens":10,"output_tokens":5}
		}`)
		out, err := TranslateClaudeResponseToOpenAI(claudeJSON)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		var resp struct {
			Choices []struct {
				Message struct {
					ToolCalls []map[string]any `json:"tool_calls"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(out, &resp); err != nil {
			t.Fatalf("unmarshal output: %v (out=%s)", err, out)
		}
		if len(resp.Choices[0].Message.ToolCalls) != 1 {
			t.Fatalf("tool_calls = %v, want one", resp.Choices[0].Message.ToolCalls)
		}
		fn, _ := resp.Choices[0].Message.ToolCalls[0]["function"].(map[string]any)
		if fn["name"] != "get_weather" {
			t.Errorf("tool name = %v, want get_weather", fn["name"])
		}
		if resp.Choices[0].FinishReason != "tool_calls" {
			t.Errorf("finish_reason = %q, want tool_calls", resp.Choices[0].FinishReason)
		}
	})
}

func TestTranslateClaudeChunkToOpenAI_StructuredOutputUnwrap(t *testing.T) {
	state := &ClaudeToOpenAIStreamState{}
	// message_start emits the role chunk
	if _, err := TranslateClaudeChunkToOpenAI([]byte(`{"type":"message_start","message":{"id":"msg_1","role":"assistant","model":"claude-sonnet-4-5","usage":{"input_tokens":10,"output_tokens":0}}}`), state); err != nil {
		t.Fatalf("message_start: %v", err)
	}
	// tool_use block start for the forced tool: no tool call, empty content
	out, err := TranslateClaudeChunkToOpenAI([]byte(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"9router_structured_output","input":{}}}`), state)
	if err != nil {
		t.Fatalf("block start: %v", err)
	}
	if strings.Contains(string(out), "tool_calls") {
		t.Errorf("forced tool block start must not emit a tool call: %s", out)
	}
	if !strings.Contains(string(out), `"content":""`) {
		t.Errorf("expected empty content chunk, got: %s", out)
	}
	// input_json_delta: the JSON lands in content
	out, err = TranslateClaudeChunkToOpenAI([]byte(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"answer\":42}"}}`), state)
	if err != nil {
		t.Fatalf("json delta: %v", err)
	}
	if !strings.Contains(string(out), `"content":"{\"answer\":42}"`) {
		t.Errorf("expected JSON in content, got: %s", out)
	}
	if strings.Contains(string(out), "tool_calls") {
		t.Errorf("json delta must not emit a tool call: %s", out)
	}
	// tool_use stop_reason maps to finish_reason stop
	out, err = TranslateClaudeChunkToOpenAI([]byte(`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`), state)
	if err != nil {
		t.Fatalf("message_delta: %v", err)
	}
	if !strings.Contains(string(out), `"finish_reason":"stop"`) {
		t.Errorf("expected finish_reason stop, got: %s", out)
	}
	if strings.Contains(string(out), `"finish_reason":"tool_calls"`) {
		t.Errorf("must not report tool_calls: %s", out)
	}
}

func TestTranslateClaudeChunkToOpenAI_ClientToolNotUnwrapped(t *testing.T) {
	state := &ClaudeToOpenAIStreamState{}
	if _, err := TranslateClaudeChunkToOpenAI([]byte(`{"type":"message_start","message":{"id":"msg_2","role":"assistant","model":"claude-sonnet-4-5","usage":{"input_tokens":10,"output_tokens":0}}}`), state); err != nil {
		t.Fatalf("message_start: %v", err)
	}
	out, err := TranslateClaudeChunkToOpenAI([]byte(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_2","name":"get_weather","input":{}}}`), state)
	if err != nil {
		t.Fatalf("block start: %v", err)
	}
	if !strings.Contains(string(out), `"name":"get_weather"`) || !strings.Contains(string(out), "tool_calls") {
		t.Errorf("client tool must emit a tool call: %s", out)
	}
	out, err = TranslateClaudeChunkToOpenAI([]byte(`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`), state)
	if err != nil {
		t.Fatalf("message_delta: %v", err)
	}
	if !strings.Contains(string(out), `"finish_reason":"tool_calls"`) {
		t.Errorf("expected finish_reason tool_calls, got: %s", out)
	}
}
