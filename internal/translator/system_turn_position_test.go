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
// stays in history every later turn fails too. Its text moves into the system
// field and the turn goes away.
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
	system, _ := req["system"].(string)
	if !strings.Contains(system, "base rules") || !strings.Contains(system, "has not heard from you") {
		t.Errorf("both the existing rules and the lifted reminder must survive, got %q", system)
	}
}

// A block-array system turn keeps its text, and a block-array system field
// gains a text block rather than being replaced.
func TestSanitizeClaudePassthrough_LiftsSystemBlocks(t *testing.T) {
	body := []byte(`{"model":"m","max_tokens":16,
		"system":[{"type":"text","text":"base"}],
		"messages":[
			{"role":"user","content":"one"},
			{"role":"assistant","content":"two"},
			{"role":"system","content":[{"type":"text","text":"late reminder"}]},
			{"role":"user","content":"three"}
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
