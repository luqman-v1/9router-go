package translator

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

func TestRepairTruncatedJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantKeys []string
		valid    bool
	}{
		{
			name:     "already valid",
			input:    `{"command":"ls -la"}`,
			wantKeys: []string{"command"},
			valid:    true,
		},
		{
			name:     "truncated inside string literal",
			input:    `{"command":"cat << 'EOF'\nsome script content`,
			wantKeys: []string{"command"},
			valid:    true,
		},
		{
			name:     "truncated after key",
			input:    `{"command":`,
			wantKeys: []string{"command"},
			valid:    true,
		},
		{
			name:     "truncated after comma",
			input:    `{"command":"echo hi",`,
			wantKeys: []string{"command"},
			valid:    true,
		},
		{
			name:     "nested object and array truncated",
			input:    `{"outer":{"arr":[{"id":1},{"id":2`,
			wantKeys: []string{"outer"},
			valid:    true,
		},
		{
			name:     "trailing escape in string",
			input:    `{"command":"echo \"hello\\`,
			wantKeys: []string{"command"},
			valid:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repaired := RepairTruncatedJSON(tt.input)
			var m map[string]any
			err := json.Unmarshal([]byte(repaired), &m)
			if tt.valid && err != nil {
				t.Fatalf("expected valid JSON after repair, got error: %v (repaired: %q)", err, repaired)
			}
			for _, k := range tt.wantKeys {
				if _, ok := m[k]; !ok {
					t.Errorf("expected key %q in repaired JSON %s", k, repaired)
				}
			}
		})
	}
}

func TestTranslateOpenAIToClaude_TruncatedToolArgs(t *testing.T) {
	// Simulates the exact user error: model hit max_tokens / length cutoff
	// while generating a long bash command.
	truncatedArgs := `{"command":"cat << 'EOF'\\n` + strings.Repeat("long script line\\n", 1000)
	encodedArgs, _ := json.Marshal(truncatedArgs)
	input := []byte(`{
		"id": "chatcmpl-trunc",
		"model": "gpt-4o",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": "",
				"tool_calls": [{
					"id": "call_1",
					"type": "function",
					"function": {
						"name": "Bash",
						"arguments": ` + string(encodedArgs) + `
					}
				}]
			},
			"finish_reason": "length"
		}],
		"usage": {"prompt_tokens": 100, "completion_tokens": 4096}
	}`)

	out, _, err := TranslateOpenAIToClaude(input)
	if err != nil {
		t.Fatalf("TranslateOpenAIToClaude failed on truncated tool args: %v", err)
	}

	var claudeResp struct {
		Content []struct {
			Type  string         `json:"type"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(out, &claudeResp); err != nil {
		t.Fatalf("failed to unmarshal Claude response: %v", err)
	}
	if len(claudeResp.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(claudeResp.Content))
	}
	if claudeResp.Content[0].Type != "tool_use" {
		t.Errorf("expected tool_use, got %q", claudeResp.Content[0].Type)
	}
	cmd, ok := claudeResp.Content[0].Input["command"].(string)
	if !ok || !strings.HasPrefix(cmd, "cat << 'EOF'") {
		t.Errorf("expected command to start with cat << 'EOF', got: %v", claudeResp.Content[0].Input)
	}
}

func TestSafeToolCallInput_AlwaysValid(t *testing.T) {
	cases := []string{
		"",
		"   ",
		"null",
		"not json at all",
		`{"unclosed":"string`,
		`{"arr":[1,2,`,
		`{"nested":{"a":`,
		`{"command":"echo \"test\\`,
	}
	for _, in := range cases {
		val := SafeToolCallInput(in)
		if !val.IsValid() {
			t.Errorf("SafeToolCallInput(%q) produced invalid JSON: %s", in, string(val))
		}
	}
}
