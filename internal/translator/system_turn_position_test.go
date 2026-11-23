package translator

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

func decodeBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return req
}

func rolesOf(req map[string]any) []string {
	msgs, _ := req["messages"].([]any)
	roles := make([]string, 0, len(msgs))
	for _, mRaw := range msgs {
		msg, _ := mRaw.(map[string]any)
		role, _ := msg["role"].(string)
		roles = append(roles, role)
	}
	return roles
}

// A reminder injected mid-history is refused by the API, and because the turn
// stays in history every later turn fails too. Its text moves into the adjacent
// user turn — not the system field, which would bust the prompt cache for the
// whole history every time a new reminder appears.
func TestSanitizeClaudePassthrough_LiftsMidArraySystemTurn(t *testing.T) {
	body := []byte(`{"model":"m","max_tokens":16,"system":"base rules","messages":[
		{"role":"user","content":"one"},
		{"role":"assistant","content":"two"},
		{"role":"system","content":"The user has not heard from you in a while."},
		{"role":"user","content":"three"}
	]}`)

	out := SanitizeClaudePassthrough(body, false)
	req := decodeBody(t, out)

	if got := rolesOf(req); len(got) != 3 || got[0] != "user" || got[1] != "assistant" || got[2] != "user" {
		t.Fatalf("system turn should be gone, roles = %v", got)
	}
	if system, _ := req["system"].(string); system != "base rules" {
		t.Errorf("system field must stay byte-stable for the cache, got %q", system)
	}
	msgs, _ := req["messages"].([]any)
	last, _ := msgs[2].(map[string]any)
	blocks, _ := last["content"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("user turn should carry its text plus the reminder, got %#v", last["content"])
	}
	first, _ := blocks[0].(map[string]any)
	second, _ := blocks[1].(map[string]any)
	if first["text"] != "three" || !strings.Contains(second["text"].(string), "has not heard from you") {
		t.Errorf("unexpected blocks %#v", blocks)
	}
}

// A reminder right after a user turn joins that turn, after its tool_result
// blocks, so the earlier history and the system field stay untouched.
func TestSanitizeClaudePassthrough_LiftsSystemTurnIntoPreviousUser(t *testing.T) {
	body := []byte(`{"model":"m","max_tokens":16,"system":"base","messages":[
		{"role":"user","content":"one"},
		{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]},
		{"role":"system","content":"reminder"}
	]}`)
	req := decodeBody(t, SanitizeClaudePassthrough(body, false))
	if system, _ := req["system"].(string); system != "base" {
		t.Errorf("system = %q", system)
	}
	msgs, _ := req["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("roles = %v", rolesOf(req))
	}
	blocks, _ := msgs[2].(map[string]any)["content"].([]any)
	if len(blocks) != 2 || blocks[0].(map[string]any)["type"] != "tool_result" {
		t.Fatalf("tool_result must stay first, got %#v", blocks)
	}
	if !strings.Contains(blocks[1].(map[string]any)["text"].(string), "reminder") {
		t.Errorf("reminder missing: %#v", blocks[1])
	}
}

// With no user turn on either side, the text falls back to the system field;
// a block-array system field gains a text block rather than being replaced.
func TestSanitizeClaudePassthrough_LiftsSystemBlocks(t *testing.T) {
	body := []byte(`{"model":"m","max_tokens":16,
		"system":[{"type":"text","text":"base"}],
		"messages":[
			{"role":"user","content":"one"},
			{"role":"assistant","content":"two"},
			{"role":"system","content":[{"type":"text","text":"late reminder"}]},
			{"role":"assistant","content":"three"}
		]}`)

	req := decodeBody(t, SanitizeClaudePassthrough(body, false))
	blocks, ok := req["system"].([]any)
	if !ok || len(blocks) != 2 {
		t.Fatalf("system should hold both blocks, got %#v", req["system"])
	}
	last, _ := blocks[1].(map[string]any)
	if text, _ := last["text"].(string); text != "late reminder" {
		t.Errorf("lifted text = %q", text)
	}
}

// Clean history stays byte-identical; the pass only fires when it has work.
func TestSanitizeClaudePassthrough_SystemAtHeadUntouched(t *testing.T) {
	body := []byte(`{"model":"m","max_tokens":16,"messages":[{"role":"system","content":"rules"},{"role":"user","content":"hi"}]}`)
	if out := SanitizeClaudePassthrough(body, false); string(out) != string(body) {
		t.Fatalf("body should be untouched, got %s", out)
	}
}

// output_config is legal only on the directive-only turn. On a turn that does
// carry content the API answers "messages.<n>.output_config: Extra inputs are
// not permitted" and refuses the whole request, so the key is stripped there.
func TestSanitizeClaudePassthrough_StripsOutputConfigFromContentTurn(t *testing.T) {
	body := []byte(`{"model":"m","max_tokens":16,"messages":[
		{"role":"user","content":"one","output_config":{"format":"text"}},
		{"role":"assistant","content":"two"}
	]}`)

	req := decodeBody(t, SanitizeClaudePassthrough(body, false))
	msgs, _ := req["messages"].([]any)
	first, _ := msgs[0].(map[string]any)
	if _, ok := first["output_config"]; ok {
		t.Error("output_config on a content turn must be stripped")
	}
	if first["content"] != "one" {
		t.Errorf("content must survive, got %#v", first["content"])
	}
}
