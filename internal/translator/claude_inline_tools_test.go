package translator

import (
	"strings"
	"testing"
)

// An inline-tools block is content: a turn holding only a tool_addition must not
// read as empty and disappear from the history. The API accepts the block behind
// the inline-tools-2026-09-15 beta, so the gateway must keep it and send the flag.
func TestSanitizeClaudePassthrough_KeepsToolAdditionTurn(t *testing.T) {
	body := []byte(`{"model":"m","max_tokens":16,"messages":[
		{"role":"user","content":"one"},
		{"role":"assistant","content":[{"type":"tool_addition","tool":{"type":"tool_definition","name":"zz","input_schema":{"type":"object","properties":{}}}}]},
		{"role":"user","content":"two"}
	]}`)

	out := SanitizeClaudePassthrough(body, false)
	if !strings.Contains(string(out), "tool_addition") {
		t.Fatalf("the tool_addition turn must survive, got %s", out)
	}
}

// The same rule covers the removal side of the beta.
func TestSanitizeClaudePassthrough_KeepsToolRemovalTurn(t *testing.T) {
	body := []byte(`{"model":"m","max_tokens":16,"messages":[
		{"role":"user","content":"one"},
		{"role":"assistant","content":[{"type":"tool_removal","name":"zz"}]},
		{"role":"user","content":"two"}
	]}`)

	out := SanitizeClaudePassthrough(body, false)
	if !strings.Contains(string(out), "tool_removal") {
		t.Fatalf("the tool_removal turn must survive, got %s", out)
	}
}
