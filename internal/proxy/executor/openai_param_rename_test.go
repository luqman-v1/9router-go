package executor

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"testing"
)

// The openai provider rule from upstream paramSupport (7c42d8f8): current
// OpenAI models answer 400 "Use 'max_completion_tokens' instead", and clients
// send max_tokens.

func TestRenameMaxTokensBody(t *testing.T) {
	var m map[string]jsontext.Value

	out := renameMaxTokensBody([]byte(`{"model":"gpt-6.1","max_tokens":1024,"messages":[]}`))
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("renamed body does not parse: %v", err)
	}
	if _, ok := m["max_tokens"]; ok {
		t.Error("max_tokens must be removed")
	}
	if string(m["max_completion_tokens"]) != "1024" {
		t.Errorf("max_completion_tokens = %s, want 1024", m["max_completion_tokens"])
	}

	out = renameMaxTokensBody([]byte(`{"max_tokens":1024,"max_completion_tokens":2048}`))
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("body does not parse: %v", err)
	}
	if string(m["max_completion_tokens"]) != "2048" {
		t.Errorf("explicit max_completion_tokens = %s, want kept 2048", m["max_completion_tokens"])
	}
	if _, ok := m["max_tokens"]; ok {
		t.Error("max_tokens must be removed even when a target value exists")
	}

	untouched := []byte(`{"model":"gpt-4o","max_completion_tokens":512}`)
	if got := renameMaxTokensBody(untouched); string(got) != string(untouched) {
		t.Errorf("body without max_tokens changed: %s", got)
	}
	if got := renameMaxTokensBody([]byte(`not json`)); string(got) != "not json" {
		t.Errorf("unparsable body changed: %s", got)
	}
}
