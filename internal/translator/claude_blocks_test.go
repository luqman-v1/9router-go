package translator

import "testing"

// The emptiness rule is a denylist: only shapes known to carry nothing are
// dropped. A block type the gateway has never seen is content by definition,
// so it reaches the provider instead of vanishing (D1/D2 of the passthrough
// integrity design).
func TestKnownEmptyBlock_UnknownTypeIsContent(t *testing.T) {
	cases := []struct {
		name  string
		block map[string]any
		want  bool // contentful
	}{
		{"unknown type survives", map[string]any{"type": "future_block", "payload": 1}, true},
		{"blank text is empty", map[string]any{"type": "text", "text": "  "}, false},
		{"text with words is content", map[string]any{"type": "text", "text": "hi"}, true},
		{"unsigned thinking is empty", map[string]any{"type": "thinking", "thinking": "x"}, false},
		{"tool_use with null input is content", map[string]any{"type": "tool_use", "name": "Bash", "input": nil}, true},
		{"empty tool_result is empty", map[string]any{"type": "tool_result", "content": []any{}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isContentfulBlock(tc.block); got != tc.want {
				t.Errorf("isContentfulBlock = %v, want %v", got, tc.want)
			}
		})
	}
}

// The sanitizer's caller forwards its bytes untouched when nothing changed, so
// a conversation whose only oddity is a block type the gateway has never seen
// must come back exactly as it went in.
func TestSanitizeClaudePassthrough_UnknownBlockIsUntouched(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":64,"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"future_block","payload":1}]},{"role":"user","content":"again"}]}`)
	if out := SanitizeClaudePassthrough(body, false); string(out) != string(body) {
		t.Errorf("unknown block body was rewritten:\n got %s\nwant %s", out, body)
	}
}

// tool_result shapes: only an empty array or blank string is empty; anything
// else it carries — including an array of blocks — is content.
func TestKnownEmptyBlock_ToolResultShapes(t *testing.T) {
	cases := []struct {
		name    string
		content any
		want    bool // empty
	}{
		{"empty array", []any{}, true},
		{"empty string", "", true},
		{"blank string", "  ", true},
		{"string output", "42", false},
		{"block array", []any{map[string]any{"type": "text", "text": "hi"}}, false},
		{"no content key", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			block := map[string]any{"type": "tool_result", "tool_use_id": "toolu_1"}
			if tc.name != "no content key" {
				block["content"] = tc.content
			}
			if got := knownEmptyBlock(block); got != tc.want {
				t.Errorf("knownEmptyBlock = %v, want %v", got, tc.want)
			}
		})
	}
}
