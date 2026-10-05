package translator

import (
	json "encoding/json/v2"
	"testing"
)

// --- TranslateClaudeToOpenAI: strict survives Claude -> OpenAI ---

// An explicit tool strict is a client instruction, so it must reach the OpenAI
// function verbatim; an unset one must stay unset, because emitting `false`
// there would silently opt a lenient call into strict validation.
func TestTranslateClaudeToOpenAIToolStrict(t *testing.T) {
	tests := []struct {
		name string
		tool string
		want any // nil means "key must be absent"
	}{
		{"strict true", `{"name":"probe","strict":true,"input_schema":{"type":"object","properties":{}}}`, true},
		{"strict false", `{"name":"probe","strict":false,"input_schema":{"type":"object","properties":{}}}`, false},
		{"strict absent", `{"name":"probe","input_schema":{"type":"object","properties":{}}}`, nil},
		{"strict non-boolean is not an instruction", `{"name":"probe","strict":"yes","input_schema":{"type":"object","properties":{}}}`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := TranslateClaudeToOpenAI([]byte(`{"model":"gpt-5.5","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"tools":[` + tt.tool + `]}`))
			if err != nil {
				t.Fatalf("TranslateClaudeToOpenAI: %v", err)
			}
			var req struct {
				Tools []struct {
					Function map[string]any `json:"function"`
				} `json:"tools"`
			}
			if err := json.Unmarshal(out, &req); err != nil {
				t.Fatalf("decode %s: %v", out, err)
			}
			if len(req.Tools) != 1 {
				t.Fatalf("got %d tools, want 1", len(req.Tools))
			}
			fn := req.Tools[0].Function
			if fn["name"] != "probe" {
				t.Errorf("name = %v, want probe", fn["name"])
			}
			if _, ok := fn["parameters"]; !ok {
				t.Error("parameters missing")
			}
			if tt.want == nil {
				if _, present := fn["strict"]; present {
					t.Errorf("strict must stay absent, got %v", fn["strict"])
				}
				return
			}
			strict, ok := fn["strict"].(bool)
			if !ok {
				t.Fatalf("strict missing, want %v", tt.want)
			}
			if strict != tt.want {
				t.Errorf("strict = %v, want %v", strict, tt.want)
			}
		})
	}
}

// ClaudeTool/OpenAIFunction carry strict as *bool, so an explicit false still
// serializes and an absent one produces no key at all.
func TestToolStrictSerialization(t *testing.T) {
	tests := []struct {
		name string
		tool ClaudeTool
		want string
	}{
		{"true emits strict", ClaudeTool{Name: "probe", Strict: new(true)}, `{"name":"probe","strict":true}`},
		{"false emits strict", ClaudeTool{Name: "probe", Strict: new(false)}, `{"name":"probe","strict":false}`},
		{"absent emits no strict", ClaudeTool{Name: "probe"}, `{"name":"probe"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := json.Marshal(tt.tool)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(b) != tt.want {
				t.Errorf("marshal = %s, want %s", b, tt.want)
			}
		})
	}
}