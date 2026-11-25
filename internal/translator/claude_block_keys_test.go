package translator

import (
	json "encoding/json/v2"
	"testing"
)

// Anthropic defines `caller` on the tool_use block only and refuses it
// anywhere else with "messages.<n>.content.<m>.text.caller: Extra inputs are
// not permitted" — one bad block poisons the rest of the session. This pins
// the split: dropped from text and from text nested in a tool_result, kept on
// tool_use.
func TestSanitizeClaudePassthrough_DropsCallerOutsideToolUse(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5-5","messages":[` +
		`{"role":"user","content":[{"type":"text","text":"hi","caller":{"type":"agent"}}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_01abc","name":"Read","input":{},"caller":{"type":"agent","agent_id":"a1"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01abc","content":[{"type":"text","text":"ok","caller":{"type":"agent"}}]}]}` +
		`]}`)
	got := SanitizeClaudePassthrough(body, false)
	var decoded struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("unmarshal sanitized body: %v (%s)", err, got)
	}
	if n := len(decoded.Messages); n != 3 {
		t.Fatalf("messages dropped, got %d: %s", n, got)
	}
	if _, has := decoded.Messages[0]["content"].([]any)[0].(map[string]any)["caller"]; has {
		t.Errorf("text block kept caller: %s", got)
	}
	toolUse := decoded.Messages[1]["content"].([]any)[0].(map[string]any)
	if _, has := toolUse["caller"]; !has {
		t.Errorf("tool_use lost its valid caller: %s", got)
	}
	nested := decoded.Messages[2]["content"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if _, has := nested["caller"]; has {
		t.Errorf("text nested in tool_result kept caller: %s", got)
	}
}

// A body whose only caller is on tool_use must be forwarded byte for byte.
func TestSanitizeClaudePassthrough_ValidCallerUntouched(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5-5","messages":[` +
		`{"role":"user","content":"hi"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_01abc","name":"Read","input":{},"caller":{"type":"agent"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01abc","content":"ok"}]}` +
		`]}`)
	if got := SanitizeClaudePassthrough(body, false); string(got) != string(body) {
		t.Errorf("a body with only valid callers was rewritten:\n got: %s\nwant: %s", got, body)
	}
}
