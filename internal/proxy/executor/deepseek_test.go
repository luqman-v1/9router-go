package executor

import (
	"encoding/json"
	"testing"
)

func TestNormalizeDeepSeekThinkingToolChoice(t *testing.T) {
	base := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"run"}}]`
	tests := []struct {
		name      string
		body      string
		wantAuto  bool
		unchanged bool
	}{
		{name: "forced required downgrades to auto", body: base + `,"tool_choice":"required"}`, wantAuto: true},
		{name: "named object downgrades to auto", body: base + `,"tool_choice":{"type":"function","function":{"name":"run"}}}`, wantAuto: true},
		{name: "auto passes through", body: base + `,"tool_choice":"auto"}`, unchanged: true},
		{name: "none passes through", body: base + `,"tool_choice":"none"}`, unchanged: true},
		{name: "no tools keeps forced choice", body: `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"tool_choice":"required"}`, unchanged: true},
		{name: "explicit disabled thinking keeps forced choice", body: base + `,"tool_choice":"required","thinking":{"type":"disabled"}}`, unchanged: true},
		{name: "reasoning effort none keeps forced choice", body: base + `,"tool_choice":"required","reasoning_effort":"none"}`, unchanged: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := NormalizeDeepSeekThinkingToolChoice([]byte(tt.body))
			if tt.unchanged {
				if string(out) != tt.body {
					t.Fatalf("body changed when it should not:\ngot  %s\nwant %s", out, tt.body)
				}
				return
			}
			var m map[string]any
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatalf("output is not valid JSON: %v", err)
			}
			if got, _ := m["tool_choice"].(string); got != "auto" {
				t.Errorf("tool_choice = %v, want auto", m["tool_choice"])
			}
		})
	}
}

func TestNormalizeDeepSeekThinkingToolChoice_MalformedJSON(t *testing.T) {
	in := []byte(`{not-json`)
	if out := NormalizeDeepSeekThinkingToolChoice(in); string(out) != string(in) {
		t.Error("malformed input must be returned untouched")
	}
}
