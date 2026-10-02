package translator

import (
	json "encoding/json/v2"
	"testing"
)

// Upstream #4316: a user turn whose only content block is `container_upload`
// (an Anthropic Files API reference) is valid input, but the passthrough's
// empty-content filter treated any block type it did not know as nothing to
// forward. The turn was dropped, the request went out as `messages: []`, and
// the provider answered 200 to a conversation that no longer existed.
func TestSanitizeClaudePassthrough_ContainerUploadKeepsTurn(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		wantBlocks int
	}{
		{
			name:       "lone container_upload block",
			content:    `[{"type":"container_upload","file_id":"file_abc123"}]`,
			wantBlocks: 1,
		},
		{
			name:       "bare container_upload object in place of the array",
			content:    `{"type":"container_upload","file_id":"file_abc123"}`,
			wantBlocks: 1,
		},
		{
			name:       "container_upload alongside a typed question",
			content:    `[{"type":"container_upload","file_id":"file_abc123"},{"type":"text","text":"summarise this"}]`,
			wantBlocks: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":64,"messages":[{"role":"user","content":` + tt.content + `}]}`)

			out := SanitizeClaudePassthrough(body, false)

			messages := claudeMessagesOf(t, out)
			if len(messages) != 1 {
				t.Fatalf("container_upload-only request must keep its user turn, got %d messages: %s", len(messages), out)
			}
			if role, _ := messages[0]["role"].(string); role != "user" {
				t.Fatalf("kept message role = %v, want user", messages[0]["role"])
			}
			// The bare-object shape stays an object here: wrapping it is the
			// cache anchor's job and the sanitizer runs before that. Either
			// shape is valid Anthropic input, so accept both.
			blocks := contentBlocksOf(t, messages[0]["content"])
			if len(blocks) != tt.wantBlocks {
				t.Fatalf("content has %d blocks, want %d: %s", len(blocks), tt.wantBlocks, out)
			}
			first, ok := blocks[0].(map[string]any)
			if !ok || first["type"] != claudeBlockContainerUpload {
				t.Fatalf("container_upload block did not survive sanitization: %s", out)
			}
			if first["file_id"] != "file_abc123" {
				t.Errorf("file_id = %v, want file_abc123 — the block must reach the provider untouched", first["file_id"])
			}
		})
	}
}

// The filter the container_upload fix widened is still a filter: a turn that
// genuinely carries nothing Anthropic accepts must not survive it.
func TestSanitizeClaudePassthrough_DropsGenuinelyEmptyTurns(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantDropped bool
	}{
		{name: "empty block array", content: `[]`, wantDropped: true},
		{name: "blank string", content: `"   "`, wantDropped: true},
		{name: "whitespace-only text", content: `[{"type":"text","text":"   "}]`, wantDropped: true},
		{name: "typed turn beside a blank block", content: `[{"type":"text","text":"hi"},{"type":"text","text":""}]`, wantDropped: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":` + tt.content + `}]}`)

			messages := claudeMessagesOf(t, SanitizeClaudePassthrough(body, false))

			if got := len(messages) == 0; got != tt.wantDropped {
				t.Errorf("dropped = %v, want %v (kept %d messages)", got, tt.wantDropped, len(messages))
			}
		})
	}
}

// claudeMessagesOf decodes the messages array of a Claude request body, failing
// the test rather than handing back a nil slice a length check could not tell
// apart from a genuinely empty conversation.
func claudeMessagesOf(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var req struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	return req.Messages
}

// contentBlocksOf reads a message's content as a list, tolerating the bare
// content-block object some clients send in place of the array.
func contentBlocksOf(t *testing.T, content any) []any {
	t.Helper()
	switch c := content.(type) {
	case []any:
		return c
	case map[string]any:
		return []any{c}
	default:
		t.Fatalf("content is %T, want a block array or a block object", content)
		return nil
	}
}
