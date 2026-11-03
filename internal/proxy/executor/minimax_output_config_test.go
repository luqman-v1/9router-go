package executor

import (
	json "encoding/json/v2"
	"testing"
)

func TestEnsureClaudeMessagesFor_MinimaxStripsOutputConfig(t *testing.T) {
	input := `{"model":"m","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high","output_config":{"effort":"high"}}`

	// For minimax-code: output_config must be stripped
	out := EnsureClaudeMessagesFor([]byte(input), "minimax-code", "m")
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if _, exists := m["output_config"]; exists {
		t.Errorf("expected output_config stripped for minimax-code, but found %v", m["output_config"])
	}

	// For standard claude/anthropic: output_config is retained
	claudeOut := EnsureClaudeMessagesFor([]byte(input), "claude", "claude-haiku-5-5")
	var claudeMap map[string]any
	if err := json.Unmarshal(claudeOut, &claudeMap); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if _, exists := claudeMap["output_config"]; !exists {
		t.Errorf("expected output_config preserved for claude, but was missing")
	}
}

func TestStripMinimaxOutputConfig_ClaudePassthrough(t *testing.T) {
	input := []byte(`{"model":"abab6.5s-chat","messages":[{"role":"user","content":"hi"}],"output_config":{"effort":"high"}}`)
	stripped := stripMinimaxOutputConfig(input)

	var m map[string]any
	if err := json.Unmarshal(stripped, &m); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if _, exists := m["output_config"]; exists {
		t.Errorf("expected output_config stripped, but found %v", m["output_config"])
	}
}
