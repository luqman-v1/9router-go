package translator

import (
	json "encoding/json/v2"
	"testing"
)

// Claude states tool policy in two places: tool_choice.type carries "none"
// (#4577), and tool_choice.disable_parallel_tool_use carries the
// single-tool-call policy OpenAI writes as parallel_tool_calls:false (#4581).

func TestConvertToolChoice_NoneIsPreserved(t *testing.T) {
	got := convertToolChoice(rawPtr(`{"type":"none"}`))
	if got != "none" {
		t.Errorf("tool_choice none = %v, want \"none\" (tools must stay forbidden)", got)
	}
}

func TestConvertToolChoice_PolicyShapes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  any
	}{
		{"auto", `{"type":"auto"}`, "auto"},
		{"any", `{"type":"any"}`, "required"},
		{"none", `{"type":"none"}`, "none"},
		{"tool", `{"type":"tool","name":"probe"}`, map[string]any{
			"type":     "function",
			"function": map[string]any{"name": "probe"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convertToolChoice(rawPtr(tt.input))
			// Deterministic encoding: Go map iteration order would otherwise
			// make this comparison flaky.
			enc := json.Deterministic(true)
			gotJSON, err := json.Marshal(got, enc)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			wantJSON, err := json.Marshal(tt.want, enc)
			if err != nil {
				t.Fatalf("marshal want: %v", err)
			}
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("convertToolChoice(%s) = %s, want %s", tt.input, gotJSON, wantJSON)
			}
		})
	}
}

// The single-tool-call policy must not disturb tool_choice itself: it travels
// as a separate OpenAI field, and an absent policy stays absent.
func TestTranslateClaudeToOpenAI_ParallelToolPolicy(t *testing.T) {
	tests := []struct {
		name       string
		toolChoice string
		wantChoice any
		wantPar    bool // parallel_tool_calls must be false
		wantAbsent bool // parallel_tool_calls must not be emitted
	}{
		{
			name:       "disable flag becomes parallel_tool_calls false",
			toolChoice: `{"type":"any","disable_parallel_tool_use":true}`,
			wantChoice: "required", wantPar: true,
		},
		{
			name:       "tool choice without the flag adds no policy",
			toolChoice: `{"type":"auto"}`,
			wantChoice: "auto", wantAbsent: true,
		},
		{
			name:       "explicit false is not a policy",
			toolChoice: `{"type":"auto","disable_parallel_tool_use":false}`,
			wantChoice: "auto", wantAbsent: true,
		},
		{
			name:       "named tool keeps its name alongside the policy",
			toolChoice: `{"type":"tool","name":"probe","disable_parallel_tool_use":true}`,
			wantChoice: map[string]any{
				"type":     "function",
				"function": map[string]any{"name": "probe"},
			}, wantPar: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{
				"model": "gpt-5.5",
				"messages": [{"role": "user", "content": "hi"}],
				"tools": [{"name": "probe", "input_schema": {"type": "object"}}],
				"tool_choice": ` + tt.toolChoice + `
			}`)
			out, err := TranslateClaudeToOpenAI(body)
			if err != nil {
				t.Fatalf("TranslateClaudeToOpenAI: %v", err)
			}
			var req OpenAIRequest
			if err := json.Unmarshal(out, &req); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			enc := json.Deterministic(true)
			gotChoice, err := json.Marshal(req.ToolChoice, enc)
			if err != nil {
				t.Fatalf("marshal choice: %v", err)
			}
			wantChoice, err := json.Marshal(tt.wantChoice, enc)
			if err != nil {
				t.Fatalf("marshal want choice: %v", err)
			}
			if string(gotChoice) != string(wantChoice) {
				t.Errorf("tool_choice = %s, want %s", gotChoice, wantChoice)
			}

			switch {
			case tt.wantAbsent && req.ParallelToolCalls != nil:
				t.Errorf("parallel_tool_calls = %v, want absent", *req.ParallelToolCalls)
			case tt.wantPar && (req.ParallelToolCalls == nil || *req.ParallelToolCalls):
				t.Errorf("parallel_tool_calls = %v, want false", req.ParallelToolCalls)
			}
		})
	}
}

// The policy flag lives on tool_choice, so a body without one has no policy to
// translate and must not grow the field.
func TestTranslateClaudeToOpenAI_NoToolChoiceAddsNoPolicy(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.5",
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [{"name": "probe", "input_schema": {"type": "object"}}]
	}`)
	out, err := TranslateClaudeToOpenAI(body)
	if err != nil {
		t.Fatalf("TranslateClaudeToOpenAI: %v", err)
	}
	var req OpenAIRequest
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.ParallelToolCalls != nil {
		t.Errorf("parallel_tool_calls = %v, want absent without a tool_choice", *req.ParallelToolCalls)
	}
}
