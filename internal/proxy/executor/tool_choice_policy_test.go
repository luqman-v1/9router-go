package executor

import (
	json "encoding/json/v2"
	"encoding/json/jsontext"
	"testing"
)

// ensureMessagesMaxTokens is the OpenAI -> Claude Messages path, where the
// caller's tool policy has to survive: "none" must not be upgraded to tool
// permission (#4577) and parallel_tool_calls:false must become Claude's
// disable_parallel_tool_use (#4581).

func claudeToolChoiceOf(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	tc, _ := m["tool_choice"].(map[string]any)
	return tc
}

func TestEnsureMessagesMaxTokens_ToolChoiceNoneKeepsToolsForbidden(t *testing.T) {
	input := []byte(`{
		"model": "combo-wombo",
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [{"type": "function", "function": {"name": "probe", "parameters": {"type": "object"}}}],
		"tool_choice": "none"
	}`)

	tc := claudeToolChoiceOf(t, ensureMessagesMaxTokens(input, "claude-sonnet-4-5"))
	if tc == nil {
		t.Fatal("tool_choice was dropped; Claude would read the request as \"tools allowed\"")
	}
	if tc["type"] != "none" {
		t.Errorf("tool_choice.type = %v, want \"none\"", tc["type"])
	}
}

func TestConvertToolChoiceToClaude(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want any
	}{
		{"auto", "auto", map[string]any{"type": "auto"}},
		{"required", "required", map[string]any{"type": "any"}},
		{"none", "none", map[string]any{"type": "none"}},
		{"named function", map[string]any{"type": "function", "function": map[string]any{"name": "probe"}},
			map[string]any{"type": "tool", "name": "probe"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Deterministic encoding: Go map iteration order would otherwise
			// make this comparison flaky.
			enc := json.Deterministic(true)
			got, err := json.Marshal(convertToolChoiceToClaude(tt.in), enc)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			want, err := json.Marshal(tt.want, enc)
			if err != nil {
				t.Fatalf("marshal want: %v", err)
			}
			if string(got) != string(want) {
				t.Errorf("convertToolChoiceToClaude(%v) = %s, want %s", tt.in, got, want)
			}
		})
	}
}

const parallelToolsJSON = `[
	{"type": "function", "function": {"name": "probe", "parameters": {"type": "object"}}}
]`

func TestEnsureMessagesMaxTokens_ParallelToolCallsPolicy(t *testing.T) {
	tests := []struct {
		name         string
		tools        string
		toolChoice   string
		wantType     string
		wantName     string
		wantPolicy   bool // disable_parallel_tool_use must be true
		wantNoPolicy bool // tool_choice kept, but without the restriction
		wantNoChoice bool // tool_choice must stay absent
	}{
		{
			name:       "explicit auto keeps auto",
			tools:      parallelToolsJSON,
			toolChoice: `"auto"`,
			wantType:   "auto", wantPolicy: true,
		},
		{
			name:       "required becomes any",
			tools:      parallelToolsJSON,
			toolChoice: `"required"`,
			wantType:   "any", wantPolicy: true,
		},
		{
			name:     "implicit choice synthesises auto",
			tools:    parallelToolsJSON,
			wantType: "auto", wantPolicy: true,
		},
		{
			name:       "named tool choice is merged, not clobbered",
			tools:      parallelToolsJSON,
			toolChoice: `{"type": "function", "function": {"name": "probe"}}`,
			wantType:   "tool", wantName: "probe", wantPolicy: true,
		},
		{
			name:         "no tools adds no restriction",
			tools:        `[]`,
			toolChoice:   `"auto"`,
			wantType:     "auto",
			wantNoPolicy: true,
		},
		{
			name:         "no tools and no tool choice adds no restriction",
			tools:        `[]`,
			wantNoChoice: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Built as a map rather than string-concatenated: an empty
			// tool_choice fragment left a bare comma in the literal, which the
			// strict jsontext decoder rejects.
			body := map[string]any{
				"model":                "combo-wombo",
				"messages":             []any{map[string]any{"role": "user", "content": "hi"}},
				"tools":                jsontext.Value(tt.tools),
				"parallel_tool_calls": false,
			}
			if tt.toolChoice != "" {
				body["tool_choice"] = jsontext.Value(tt.toolChoice)
			}
			input, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("marshal body: %v", err)
			}
			out := ensureMessagesMaxTokens(input, "claude-sonnet-4-5")

			var m map[string]any
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if _, has := m["parallel_tool_calls"]; has {
				t.Error("parallel_tool_calls is OpenAI-only and must not reach Claude")
			}

			tc := claudeToolChoiceOf(t, out)
			if tt.wantNoChoice {
				if tc != nil {
					t.Errorf("tool_choice = %v, want absent", tc)
				}
				return
			}
			if tc == nil {
				t.Fatal("tool_choice was dropped, losing the single-tool-call policy")
			}
			if tc["type"] != tt.wantType {
				t.Errorf("tool_choice.type = %v, want %q", tc["type"], tt.wantType)
			}
			if tt.wantNoPolicy {
				if _, has := tc["disable_parallel_tool_use"]; has {
					t.Errorf("tool_choice = %v, want no restriction added", tc)
				}
				return
			}
			if tt.wantName != "" && tc["name"] != tt.wantName {
				t.Errorf("tool_choice.name = %v, want %q (the explicit choice must survive)", tc["name"], tt.wantName)
			}
			if tc["disable_parallel_tool_use"] != true {
				t.Errorf("disable_parallel_tool_use = %v, want true", tc["disable_parallel_tool_use"])
			}
		})
	}
}

// Without the policy flag there is nothing to translate, and the OpenAI-only
// key must still be stripped from the Claude payload.
func TestEnsureMessagesMaxTokens_ParallelToolCallsTrueAddsNoPolicy(t *testing.T) {
	input := []byte(`{
		"model": "combo-wombo",
		"messages": [{"role": "user", "content": "hi"}],
		"tools": ` + parallelToolsJSON + `,
		"tool_choice": "auto",
		"parallel_tool_calls": true
	}`)
	out := ensureMessagesMaxTokens(input, "claude-sonnet-4-5")

	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, has := m["parallel_tool_calls"]; has {
		t.Error("parallel_tool_calls is OpenAI-only and must not reach Claude")
	}
	tc := claudeToolChoiceOf(t, out)
	if _, has := tc["disable_parallel_tool_use"]; has {
		t.Errorf("tool_choice = %v, want no restriction added", tc)
	}
}
