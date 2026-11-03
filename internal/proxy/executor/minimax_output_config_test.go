package executor

import (
	json "encoding/json/v2"
	"testing"
)

func TestEnsureClaudeMessagesFor_MinimaxStripsOutputConfig(t *testing.T) {
	input := `{"model":"m","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high","output_config":{"effort":"high"}}`

	// For minimax: output_config must be stripped
	out := EnsureClaudeMessagesFor([]byte(input), "minimax", "m")
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if _, exists := m["output_config"]; exists {
		t.Errorf("expected output_config stripped for minimax, but found %v", m["output_config"])
	}

	// For minimax-cn: output_config must be stripped
	outCn := EnsureClaudeMessagesFor([]byte(input), "minimax-cn", "m")
	var mCn map[string]any
	if err := json.Unmarshal(outCn, &mCn); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if _, exists := mCn["output_config"]; exists {
		t.Errorf("expected output_config stripped for minimax-cn, but found %v", mCn["output_config"])
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
