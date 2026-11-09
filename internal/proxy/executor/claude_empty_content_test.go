package executor

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

// The OpenAI to Claude conversion passes the client content arrays through
// unchanged, so a turn the client left empty used to reach a Claude-shaped
// lane as an empty content array and the provider 400ed the whole request.
// The cleanup these tests pin is the same one SanitizeClaudePassthrough runs
// on the Claude-client path (translator.DropNonContentfulClaudeMessages), so
// both paths answer the same way to the same conversation.

type claudeTurnView struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

func decodeClaudeTurns(t *testing.T, body []byte) []claudeTurnView {
	t.Helper()
	var decoded struct {
		Messages []claudeTurnView `json:"messages"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal converted body: %v (%s)", err, body)
	}
	return decoded.Messages
}

// everyTurnCarriesContent is the invariant all of these cases share: whatever
// the cleanup keeps must be a turn Anthropic accepts, so no forwarded message
// may hold an empty-array or blank-string content.
func everyTurnCarriesContent(t *testing.T, turns []claudeTurnView) {
	t.Helper()
	for i, turn := range turns {
		switch content := turn.Content.(type) {
		case string:
			if strings.TrimSpace(content) == "" {
				t.Errorf("messages[%d] (%s) carries a blank string", i, turn.Role)
			}
		case []any:
			if len(content) == 0 {
				t.Errorf("messages[%d] (%s) carries an empty array", i, turn.Role)
			}
		}
	}
}

func TestEnsureClaudeMessagesFor_EmptyTurnCleanup(t *testing.T) {
	tests := []struct {
		name      string
		provider  string
		body      string
		wantRoles []string
		wantTail  string
	}{
		{
			name:      "empty assistant turn mid-conversation is dropped",
			provider:  "minimax-code",
			body:      `{"model":"m","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[]},{"role":"user","content":"again"}]}`,
			wantRoles: []string{"user", "user"},
		},
		{
			name:      "whitespace-only assistant turn is dropped",
			provider:  "minimax-code",
			body:      `{"model":"m","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"   "},{"role":"user","content":"again"}]}`,
			wantRoles: []string{"user", "user"},
		},
		{
			name:      "same cleanup on the zen messages lane",
			provider:  "opencode-zen",
			body:      `{"model":"m","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[]},{"role":"user","content":"again"}]}`,
			wantRoles: []string{"user", "user"},
		},
		{
			name:      "emptied trailing user turn is replaced by the placeholder",
			provider:  "minimax-code",
			body:      `{"model":"m","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"},{"role":"user","content":[]}]}`,
			wantRoles: []string{"user", "assistant", "user"},
			wantTail:  "Continue.",
		},
		{
			name:      "contentful conversation is left alone",
			provider:  "minimax-code",
			body:      `{"model":"m","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"},{"role":"user","content":"again"}]}`,
			wantRoles: []string{"user", "assistant", "user"},
		},
		{
			name:      "container_upload-only turn counts as content",
			provider:  "minimax-code",
			body:      `{"model":"m","messages":[{"role":"user","content":[{"type":"container_upload","file_id":"file_1"}]}]}`,
			wantRoles: []string{"user"},
		},
		{
			name:      "tool loop survives the cleanup",
			provider:  "minimax-code",
			body:      `{"model":"m","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"42"}]}`,
			wantRoles: []string{"user", "assistant", "user"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := EnsureClaudeMessagesFor([]byte(tt.body), tt.provider, "m")
			turns := decodeClaudeTurns(t, out)
			if len(turns) != len(tt.wantRoles) {
				t.Fatalf("messages = %s, want %d turns %v", out, len(tt.wantRoles), tt.wantRoles)
			}
			for i, wantRole := range tt.wantRoles {
				if turns[i].Role != wantRole {
					t.Errorf("messages[%d].role = %q, want %q (%s)", i, turns[i].Role, wantRole, out)
				}
			}
			everyTurnCarriesContent(t, turns)
			if tt.wantTail == "" {
				return
			}
			tail := turns[len(turns)-1]
			if !strings.Contains(string(mustJSON(t, tail.Content)), tt.wantTail) {
				t.Errorf("trailing turn = %v, want it to carry %q", tail.Content, tt.wantTail)
			}
		})
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	out, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %v: %v", value, err)
	}
	return out
}

// TestEnsureClaudeMessagesFor_FullyEmptiedConversationKeepsItsTurns: a body
// whose every turn is contentless has no valid form to convert to. Dropping
// the last turn too would answer with an empty messages array, which the
// Messages API rejects just as hard, so the caller turn is forwarded and the
// provider gives the reason for the turn it was sent.
func TestEnsureClaudeMessagesFor_FullyEmptiedConversationKeepsItsTurns(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"user","content":[]}]}`
	out := EnsureClaudeMessagesFor([]byte(body), "minimax-code", "m")
	turns := decodeClaudeTurns(t, out)
	if len(turns) != 1 {
		t.Fatalf("messages = %s, want the single caller turn kept", out)
	}
	if turns[0].Role != "user" {
		t.Errorf("messages[0].role = %q, want user", turns[0].Role)
	}
}
