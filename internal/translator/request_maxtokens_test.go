package translator

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"testing"
)

// The Claude lane's rename predicate must cover gpt-6.x, which rejects
// max_tokens the same way gpt-5.x does (upstream decolua/9router 7c42d8f8).

func TestTranslateClaudeToOpenAIMaxTokens(t *testing.T) {
	out, err := TranslateClaudeToOpenAI([]byte(`{"model":"gpt-6.1","max_tokens":2048,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	var m map[string]jsontext.Value
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("output does not parse: %v", err)
	}
	if _, ok := m["max_tokens"]; ok {
		t.Error("gpt-6.1 must not receive max_tokens")
	}
	if string(m["max_completion_tokens"]) != "2048" {
		t.Errorf("max_completion_tokens = %s, want 2048", m["max_completion_tokens"])
	}

	out, err = TranslateClaudeToOpenAI([]byte(`{"model":"some-other","max_tokens":128,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	m = nil
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("output does not parse: %v", err)
	}
	if _, ok := m["max_completion_tokens"]; ok {
		t.Error("unrelated model must keep max_tokens")
	}
	if string(m["max_tokens"]) != "128" {
		t.Errorf("max_tokens = %s, want 128", m["max_tokens"])
	}
}
