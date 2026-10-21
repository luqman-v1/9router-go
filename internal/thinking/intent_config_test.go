package thinking

import (
	"testing"
)

func TestNormalizeThinkingConfig(t *testing.T) {
	user := map[string]any{
		"thinking": map[string]any{"type": "enabled"},
		"messages": []any{map[string]any{"role": "assistant", "content": "x"}, map[string]any{"role": "user", "content": "y"}},
	}
	NormalizeThinkingConfig(user)
	if _, ok := user["thinking"]; !ok {
		t.Fatal("dropped thinking on user-final turn")
	}
	tool := map[string]any{
		"thinking":         map[string]any{"type": "enabled"},
		"reasoning_effort": "high",
		"messages":         []any{map[string]any{"role": "user", "content": "y"}, map[string]any{"role": "assistant", "content": "x"}},
	}
	NormalizeThinkingConfig(tool)
	if _, ok := tool["thinking"]; ok {
		t.Fatal("kept thinking on non-user-final turn")
	}
	if tool["reasoning_effort"] != "high" {
		t.Fatal("touched reasoning_effort")
	}
	empty := map[string]any{"thinking": map[string]any{"type": "enabled"}}
	NormalizeThinkingConfig(empty)
	if _, ok := empty["thinking"]; !ok {
		t.Fatal("dropped thinking with no messages")
	}
}

// TestNormalizeThinkingConfigDialectKeys dispositions the conversation-shape
// gate: `thinking` is request-scoped tool-state and drops on a non-user final
// turn through either messages or contents, while every other dialect key is
// request-level and must survive that drop.
func TestNormalizeThinkingConfigDialectKeys(t *testing.T) {
	turn := func(role string) []any {
		return []any{map[string]any{"role": role, "content": "x"}}
	}
	tests := []struct {
		name         string
		body         map[string]any
		wantThinking bool
		keep         []string
	}{
		{"messages user final keeps thinking", map[string]any{"thinking": map[string]any{"type": "enabled"}, "messages": turn("user")}, true, nil},
		{"messages assistant final drops thinking", map[string]any{"thinking": map[string]any{"type": "enabled"}, "messages": turn("assistant")}, false, nil},
		{"messages tool final drops thinking", map[string]any{"thinking": map[string]any{"type": "enabled"}, "messages": turn("tool")}, false, nil},
		{"contents user final keeps thinking", map[string]any{"thinking": map[string]any{"type": "enabled"}, "contents": turn("user")}, true, nil},
		{"contents assistant final drops thinking", map[string]any{"thinking": map[string]any{"type": "enabled"}, "contents": turn("assistant")}, false, nil},
		{"messages last not a map keeps thinking", map[string]any{"thinking": map[string]any{"type": "enabled"}, "messages": []any{"plain"}}, true, nil},
		{"messages empty keeps thinking", map[string]any{"thinking": map[string]any{"type": "enabled"}, "messages": []any{}}, true, nil},
		{"messages wrong type keeps thinking", map[string]any{"thinking": map[string]any{"type": "enabled"}, "messages": "x"}, true, nil},
		{"no conversation keeps thinking", map[string]any{"thinking": map[string]any{"type": "enabled"}}, true, nil},
		{
			name: "openai dialects survive the drop",
			body: map[string]any{
				"thinking": map[string]any{"type": "enabled"}, "reasoning_effort": "high",
				"output_config": map[string]any{"effort": "high"}, "messages": turn("assistant"),
			},
			wantThinking: false, keep: []string{"reasoning_effort", "output_config"},
		},
		{
			name: "responses and qwen dialects survive the drop",
			body: map[string]any{
				"thinking": map[string]any{"type": "enabled"}, "reasoning": map[string]any{"effort": "high"},
				"enable_thinking": true, "thinking_budget": 1024.0, "messages": turn("assistant"),
			},
			wantThinking: false, keep: []string{"reasoning", "enable_thinking", "thinking_budget"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			NormalizeThinkingConfig(tt.body)
			if _, has := tt.body["thinking"]; has != tt.wantThinking {
				t.Errorf("thinking survived = %v, want %v", has, tt.wantThinking)
			}
			for _, k := range tt.keep {
				if _, has := tt.body[k]; !has {
					t.Errorf("%q was dropped, want it kept (request-level dialect)", k)
				}
			}
		})
	}
}
