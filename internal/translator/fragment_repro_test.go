package translator

import (
	"strings"
	"testing"
)

// opencode free-tier upstream splits a single JSON object across multiple
// SSE events (blank line mid-JSON). The translator must rejoin fragments
// across chunks instead of erroring on the truncated first event.
func TestRejoinFragmentedJSONEvent(t *testing.T) {
	key := "frag-rejoin"
	defer ClearStreamState(key)

	// First half of the object (truncated JSON prefix)
	out1, err1 := TranslateOpenAIToClaudeStreamSession(key, []byte(`data: {"id":"frag","model":"deepseek","choices":[{"index":0,"delta":{"content":"hel`))
	if err1 != nil {
		t.Fatalf("partial chunk errored: %v", err1)
	}
	if out1 != nil {
		t.Fatalf("partial chunk should be held, got output: %s", out1)
	}

	// Second half completes the object
	out2, err2 := TranslateOpenAIToClaudeStreamSession(key, []byte(`data: lo"},"finish_reason":null}]}`))
	if err2 != nil {
		t.Fatalf("continuation chunk errored: %v", err2)
	}
	joined := string(out1) + string(out2)
	if !strings.Contains(joined, `"type":"message_start"`) {
		t.Errorf("expected message_start after rejoin, got: %s", joined)
	}
	if !strings.Contains(joined, `"text":"hello"`) {
		t.Errorf("expected rejoined content 'hello', got: %s", joined)
	}
}

// A genuinely malformed chunk (not truncation) must still error, and not
// pollute the pending buffer of a later valid chunk.
func TestMalformedChunkStillErrors(t *testing.T) {
	key := "frag-malformed"
	defer ClearStreamState(key)

	_, err := TranslateOpenAIToClaudeStreamSession(key, []byte(`data: {not json`))
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	// Buffer must be empty for this session afterwards.
	statesMu.Lock()
	_, has := pendingJSON[key]
	statesMu.Unlock()
	if has {
		t.Error("malformed chunk must not be buffered as a fragment")
	}
}
