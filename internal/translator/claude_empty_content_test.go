package translator

import "testing"

// DropNonContentfulClaudeMessages is the shared half of
// SanitizeClaudePassthrough (third pass plus trailing-turn repair), exported
// because the [OI] to Claude conversion path needs the same rule. These cases
// pin the helper itself, including the changed flag its callers use to decide
// whether to re-encode a body at all.
func TestDropNonContentfulClaudeMessages(t *testing.T) {
	tests := []struct {
		name          string
		messages      []any
		prefill       bool
		wantRoles     []string
		wantChanged   bool
		wantTailWords string
	}{
		{
			name:        "contentful conversation ending on a user turn reports no change",
			messages:    []any{claudeTurn("user", textBlock("hi")), claudeTurn("assistant", textBlock("yo")), claudeTurn("user", textBlock("again"))},
			wantRoles:   []string{"user", "assistant", "user"},
			wantChanged: false,
		},
		{
			name:        "empty middle turn is dropped",
			messages:    []any{claudeTurn("user", textBlock("hi")), claudeTurn("assistant"), claudeTurn("user", textBlock("again"))},
			wantRoles:   []string{"user", "user"},
			wantChanged: true,
		},
		{
			name:        "blank text turn is dropped",
			messages:    []any{claudeTurn("user", textBlock("hi")), claudeTurn("assistant", textBlock("   ")), claudeTurn("user", textBlock("again"))},
			wantRoles:   []string{"user", "user"},
			wantChanged: true,
		},
		{
			name:          "emptied trailing turn becomes the placeholder",
			messages:      []any{claudeTurn("user", textBlock("hi")), claudeTurn("assistant", textBlock("yo")), claudeTurn("user")},
			wantRoles:     []string{"user", "assistant", "user"},
			wantChanged:   true,
			wantTailWords: TrailingUserPlaceholder,
		},
		{
			name:        "client prefill is left alone",
			messages:    []any{claudeTurn("user", textBlock("hi")), claudeTurn("assistant", textBlock("yo"))},
			prefill:     true,
			wantRoles:   []string{"user", "assistant"},
			wantChanged: false,
		},
		{
			name:        "every turn empty yields an empty list",
			messages:    []any{claudeTurn("user")},
			wantRoles:   []string{},
			wantChanged: true,
		},
		{
			name: "blank thinking block with valid signature is kept",
			messages: []any{
				claudeTurn("user", textBlock("hi")),
				claudeTurn("assistant", map[string]any{"type": "thinking", "thinking": "", "signature": "EpwGCukGAXLI2nxw"}),
				claudeTurn("user", textBlock("again")),
			},
			wantRoles:   []string{"user", "assistant", "user"},
			wantChanged: false,
		},
		{
			name: "blank thinking block with invalid signature is dropped",
			messages: []any{
				claudeTurn("user", textBlock("hi")),
				claudeTurn("assistant", map[string]any{"type": "thinking", "thinking": "", "signature": "invalid_sig"}),
				claudeTurn("user", textBlock("again")),
			},
			wantRoles:   []string{"user", "user"},
			wantChanged: true,
		},
		{
			name: "redacted_thinking block is kept without signature",
			messages: []any{
				claudeTurn("user", textBlock("hi")),
				claudeTurn("assistant", map[string]any{"type": "redacted_thinking", "data": "data123"}),
				claudeTurn("user", textBlock("again")),
			},
			wantRoles:   []string{"user", "assistant", "user"},
			wantChanged: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := DropNonContentfulClaudeMessages(tt.messages, tt.prefill)
			if changed != tt.wantChanged {
				t.Errorf("changed = %v, want %v", changed, tt.wantChanged)
			}
			if len(got) != len(tt.wantRoles) {
				t.Fatalf("messages = %v, want %d turns", got, len(tt.wantRoles))
			}
			for i, wantRole := range tt.wantRoles {
				msg, ok := got[i].(map[string]any)
				if !ok {
					t.Fatalf("messages[%d] = %v, want an object", i, got[i])
				}
				if role, _ := msg["role"].(string); role != wantRole {
					t.Errorf("messages[%d].role = %q, want %q", i, role, wantRole)
				}
			}
			if tt.wantTailWords == "" {
				return
			}
			tail, _ := got[len(got)-1].(map[string]any)
			if !containsTextBlock(tail["content"], tt.wantTailWords) {
				t.Errorf("trailing content = %v, want a text block %q", tail["content"], tt.wantTailWords)
			}
		})
	}
}

func containsTextBlock(content any, want string) bool {
	blocks, ok := content.([]any)
	if !ok {
		return false
	}
	for _, bRaw := range blocks {
		block, ok := bRaw.(map[string]any)
		if !ok {
			continue
		}
		if text, _ := block["text"].(string); text == want {
			return true
		}
	}
	return false
}
