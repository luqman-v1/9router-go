package thinking

import (
	"encoding/json"
	"testing"
)

// TestOpenAICompatMaxEffort pins that OpenAI-compatible nodes expose the
// "max" effort for GPT-5.6 / GPT-6, preserving it on both the chat and
// Responses-style inputs, while clamping gpt-5 and the built-in openai lane
// to "xhigh".
func TestOpenAICompatMaxEffort(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		model    string
		body     map[string]any
		want     string
	}{
		{
			name:     "compat reasoning_effort max preserved",
			provider: "openai-compatible-chat-test",
			model:    "gpt-6-astra",
			body:     map[string]any{"model": "gpt-6-astra", "reasoning_effort": "max"},
			want:     "max",
		},
		{
			name:     "compat output_config effort max preserved",
			provider: "openai-compatible-chat-test",
			model:    "gpt-6-astra",
			body:     map[string]any{"model": "gpt-6-astra", "output_config": map[string]any{"effort": "max"}},
			want:     "max",
		},
		{
			name:     "compat gpt-5 clamped to xhigh",
			provider: "openai-compatible-chat-test",
			model:    "gpt-5",
			body:     map[string]any{"model": "gpt-5", "reasoning_effort": "max"},
			want:     "xhigh",
		},
		{
			name:     "built-in openai clamped to xhigh",
			provider: "openai",
			model:    "gpt-6-astra",
			body:     map[string]any{"model": "gpt-6-astra", "reasoning_effort": "max"},
			want:     "xhigh",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name+" Apply", func(t *testing.T) {
			out := apply(t, "openai", tc.provider, tc.model, tc.body, nil)
			got, _ := out["reasoning_effort"].(string)
			if got != tc.want {
				t.Fatalf("reasoning_effort = %q, want %q (body %+v)", got, tc.want, out)
			}
		})
		t.Run(tc.name+" ApplyJSON", func(t *testing.T) {
			raw, _ := json.Marshal(tc.body)
			out := ApplyJSON(raw, "openai", tc.provider, tc.model, nil)
			var m map[string]any
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatalf("unmarshal: %v (%s)", err, string(out))
			}
			got, _ := m["reasoning_effort"].(string)
			if got != tc.want {
				t.Fatalf("reasoning_effort = %q, want %q (body %s)", got, tc.want, string(out))
			}
		})
	}
}
