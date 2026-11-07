package executor

import (
	json "encoding/json/v2"
	"testing"
)

// The thinking pass injects `thinking` as an anthropic-style object
// ({"type":"enabled",...} / {"type":"disabled"}) while older clients send a
// plain bool. Both must parse; only "disabled" (or false) turns thinking off.
func TestDeepSeekRequestEnvelopeThinkingShapes(t *testing.T) {
	tests := []struct {
		name     string
		thinking string
		want     bool
	}{
		{name: "absent", thinking: "", want: false},
		{name: "bool true", thinking: `"thinking":true,`, want: true},
		{name: "bool false", thinking: `"thinking":false,`, want: false},
		{name: "enabled object", thinking: `"thinking":{"type":"enabled","budget_tokens":10000},`, want: true},
		{name: "disabled object", thinking: `"thinking":{"type":"disabled"},`, want: false},
		{name: "adaptive object", thinking: `"thinking":{"type":"adaptive"},`, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"halo"}],` + tt.thinking + `"reasoning_effort":""}`)
			var env deepSeekRequestEnvelope
			if err := json.Unmarshal(body, &env); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := bool(env.Thinking); got != tt.want {
				t.Errorf("Thinking = %v, want %v", got, tt.want)
			}
			opts := resolveDeepSeekModelOptions(env.Model, env)
			if tt.want && !opts.thinking {
				t.Errorf("resolveDeepSeekModelOptions thinking = false, want true")
			}
		})
	}
}
