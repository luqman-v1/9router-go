package executor

import (
	json "encoding/json/v2"
	"testing"
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
	req := buildCodexBody(t, `{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}],"tools":[`+tool+`]}`)
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
		{"nested strict absent", `{"type":"function","function":{"name":"probe","parameters":{"type":"object","properties":{}}}}`, nil},
		{"flat strict true", `{"type":"function","name":"probe","strict":true,"parameters":{"type":"object","properties":{}}}`, true},
		{"flat strict false", `{"type":"function","name":"probe","strict":false,"parameters":{"type":"object","properties":{}}}`, false},
		{"flat strict absent", `{"type":"function","name":"probe","parameters":{"type":"object","properties":{}}}`, nil},
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
			req := buildCodexBody(t, `{"model":"gpt-5.5","input":[{"type":"message","role":"user","content":"hi"}],"tools":[`+tt.tool+`]}`)
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