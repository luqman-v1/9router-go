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
		{"empty tool_result is content", map[string]any{"type": "tool_result", "content": []any{}}, true},
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

// A tool_result is never empty, whatever its content: it answers a tool_use,
// and dropping it would break that pairing.
func TestKnownEmptyBlock_ToolResultIsNeverEmpty(t *testing.T) {
	cases := []struct {
		name    string
		content any
	}{
		{"empty array", []any{}},
		{"empty string", ""},
		{"blank string", "  "},
		{"string output", "42"},
		{"block array", []any{map[string]any{"type": "text", "text": "hi"}}},
		{"no content key", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			block := map[string]any{"type": "tool_result", "tool_use_id": "toolu_1"}
			if tc.name != "no content key" {
				block["content"] = tc.content
			}
			if knownEmptyBlock(block) {
				t.Errorf("tool_result counted as empty, want content")
			}
		})
	}
}

// The pairing rule: an assistant tool_use is answered by a user turn whose only
// block is an empty tool_result. Both turns must survive the sanitizer, or the
// provider rejects the request for an unanswered tool_use.
func TestSanitizeClaudePassthrough_EmptyToolResultKeepsPairing(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":64,"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"run it"}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[]}]}]}`)

	messages := claudeMessagesOf(t, SanitizeClaudePassthrough(body, false))
	if len(messages) != 3 {
		t.Fatalf("messages = %d, want 3: %v", len(messages), messages)
	}
	wantTypes := []string{"text", "tool_use", "tool_result"}
	for i, want := range wantTypes {
		blocks := contentBlocksOf(t, messages[i]["content"])
		first, _ := blocks[0].(map[string]any)
		if first["type"] != want {
			t.Errorf("messages[%d] first block = %v, want %s", i, first["type"], want)
		}
	}
}
