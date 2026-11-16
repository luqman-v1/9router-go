package translator

import (
	json "encoding/json/v2"
	"testing"
)

// A Claude-shaped body can still reach Anthropic carrying a key a message
// object does not define — a combo that mixed lanes, or a client that replayed
// an OpenAI-shaped turn. Anthropic refuses the whole request
// ("messages.<n>.<key>: Extra inputs are not permitted"), and the turn stays in
// history, so every later request fails too. SanitizeClaudePassthrough is the
// gate every Claude passthrough goes through, so it drops the keys there.

// TestSanitizeClaudePassthrough_DropsForeignMessageKeys asserts on the body the
// upstream sees, and that the turn itself is preserved.
func TestSanitizeClaudePassthrough_DropsForeignMessageKeys(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-5","messages":[` +
		`{"role":"user","content":[{"type":"text","text":"list the files"}]},` +
		`{"role":"assistant","reasoning_content":"I should call bash",` +
		`"content":[{"type":"tool_use","id":"toolu_1","name":"bash","input":{"command":"ls"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"PROJETO.md"}]}` +
		`]}`)

	got := SanitizeClaudePassthrough(body, false)

	var decoded struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("unmarshal sanitized body: %v (%s)", err, got)
	}
	if len(decoded.Messages) != 3 {
		t.Fatalf("messages dropped, got %d: %s", len(decoded.Messages), got)
	}
	for i, msgMap := range decoded.Messages {
		for key := range msgMap {
			if key != "role" && key != "content" {
				t.Errorf("messages[%d] kept the foreign key %q: %s", i, key, got)
			}
		}
	}
	if block := decoded.Messages[1]["content"].([]any)[0].(map[string]any); block["type"] != "tool_use" {
		t.Errorf("the tool_use block was lost: %s", got)
	}
}

// A key the sweep does not know is not assumed foreign: a newer CLI may add a
// field this gateway has never heard of, and deleting it loses it silently.
// Nothing is dropped, so the sweep must report no change — otherwise the body
// is reserialized for nothing and loses its byte-for-byte pass-through.
func TestDropForeignMessageKeys_KeepsUnknownKey(t *testing.T) {
	msg := map[string]any{"role": "user", "content": "hi", "cache_config": map[string]any{"ttl": 1}}
	if dropForeignMessageKeys(msg) {
		t.Error("an unknown key is not foreign: the sweep must report no change")
	}
	if _, ok := msg["cache_config"]; !ok {
		t.Error("an unknown message key must survive to the API")
	}
}

// The known-foreign keys are still removed, on a message that carries content.
func TestDropForeignMessageKeys_RemovesKnownForeignKey(t *testing.T) {
	msg := map[string]any{"role": "user", "content": "hi", "output_config": map[string]any{"format": "text"}}
	if !dropForeignMessageKeys(msg) {
		t.Error("expected the known-foreign key sweep to report a change")
	}
	if _, ok := msg["output_config"]; ok {
		t.Error("output_config must still be dropped")
	}
	if msg["content"] != "hi" {
		t.Errorf("content must survive, got %#v", msg["content"])
	}
}

// TestSanitizeClaudePassthrough_CleanBodyUntouched: a body with nothing to
// clean must be forwarded byte for byte, so the function stays cheap and
// predictable on the hot path.
func TestSanitizeClaudePassthrough_CleanBodyUntouched(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-5","messages":[` +
		`{"role":"user","content":"hi"},` +
		`{"role":"assistant","content":"hello"},` +
		`{"role":"user","content":"again"}` +
		`]}`)

	if got := SanitizeClaudePassthrough(body, false); string(got) != string(body) {
		t.Errorf("a clean body was rewritten:\n got: %s\nwant: %s", got, body)
	}
}
