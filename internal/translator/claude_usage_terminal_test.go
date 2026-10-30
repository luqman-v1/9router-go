package translator

import (
	"fmt"
	"strings"
	"testing"
)

// countTerminalDeltas counts message_delta events in a translated SSE blob.
func countTerminalDeltas(out []byte) int {
	return strings.Count(string(out), `"type":"message_delta"`)
}

// streamClaude feeds a sequence of raw SSE payloads through one session and
// concatenates everything the translator emits.
func streamClaude(t *testing.T, key string, chunks ...string) []byte {
	t.Helper()
	var buf []byte
	for i, chunk := range chunks {
		payload := chunk
		if !strings.HasPrefix(payload, "data:") {
			payload = "data: " + payload
		}
		if !strings.HasSuffix(payload, "\n\n") {
			payload += "\n\n"
		}
		out, err := TranslateOpenAIToClaudeStreamSession(key, []byte(payload))
		if err != nil {
			t.Fatalf("chunk %d: unexpected error: %v", i, err)
		}
		buf = append(buf, out...)
	}
	return buf
}

const finishChunkFmt = `{"id":"%s","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`

// With stream_options.include_usage an OpenAI upstream sends usage on a
// trailing zero-choice chunk AFTER the finish chunk. The terminal
// message_delta must carry that usage, and the [DONE] must not duplicate it.
func TestClaudeStreamTerminalUsageAfterFinish(t *testing.T) {
	const key = "terminal-after-finish"
	defer ClearStreamState(key)

	out := streamClaude(t, key,
		`{"id":"`+key+`","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`,
		fmt.Sprintf(finishChunkFmt, key),
		`{"id":"`+key+`","model":"gpt-4o","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":40}}}`,
		"[DONE]",
	)
	s := string(out)
	// message_start legitimately reports zeros; the terminal delta must not.
	// Key order inside the marshalled map is not stable, so the assertions
	// match the values, not their positions.
	if !strings.Contains(s, `"input_tokens":60`) {
		t.Errorf("expected terminal delta input_tokens 60 (100 prompt - 40 cached), got: %s", s)
	}
	if !strings.Contains(s, `"cache_read_input_tokens":40`) {
		t.Errorf("expected cache_read_input_tokens 40, got: %s", s)
	}
	if !strings.Contains(s, `"output_tokens":7`) {
		t.Errorf("expected output_tokens 7, got: %s", s)
	}
	if got := countTerminalDeltas(out); got != 1 {
		t.Errorf("expected exactly one message_delta, got %d: %s", got, s)
	}
	if !strings.Contains(s, "data: [DONE]") {
		t.Errorf("expected [DONE] terminator, got: %s", s)
	}
}

// A well-behaved upstream that puts usage on the finish chunk itself keeps
// closing the message immediately.
func TestClaudeStreamTerminalUsageOnFinishChunk(t *testing.T) {
	const key = "terminal-on-finish"
	defer ClearStreamState(key)

	out := streamClaude(t, key,
		`{"id":"`+key+`","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`,
		`{"id":"`+key+`","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":50,"completion_tokens":3}}`,
		"[DONE]",
	)
	s := string(out)
	if !strings.Contains(s, `"input_tokens":50`) || !strings.Contains(s, `"output_tokens":3`) {
		t.Errorf("expected terminal usage on the finish chunk, got: %s", s)
	}
	if got := countTerminalDeltas(out); got != 1 {
		t.Errorf("expected exactly one message_delta, got %d: %s", got, s)
	}
}

// A stream that ends without any usage at all still closes at [DONE] — the
// terminal reports the seeded estimate, not zero.
func TestClaudeStreamTerminalEstimateFallback(t *testing.T) {
	const key = "terminal-estimate"
	defer ClearStreamState(key)
	SeedStreamPromptEstimate(key, 1234)

	out := streamClaude(t, key,
		`{"id":"`+key+`","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`,
		fmt.Sprintf(finishChunkFmt, key),
		"[DONE]",
	)
	s := string(out)
	if !strings.Contains(s, `"input_tokens":1234`) {
		t.Errorf("expected estimate fallback input_tokens 1234, got: %s", s)
	}
	if strings.Contains(s, `"cache_read_input_tokens"`) {
		t.Errorf("no upstream cache data, expected no cache fields: %s", s)
	}
	if got := countTerminalDeltas(out); got != 1 {
		t.Errorf("expected exactly one message_delta, got %d: %s", got, s)
	}
}

// A real usage report outranks the seeded estimate.
func TestClaudeStreamTerminalRealUsageBeatsEstimate(t *testing.T) {
	const key = "terminal-real-beats-estimate"
	defer ClearStreamState(key)
	SeedStreamPromptEstimate(key, 9999)

	out := streamClaude(t, key,
		`{"id":"`+key+`","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`,
		`{"id":"`+key+`","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":2}}`,
		"[DONE]",
	)
	if !strings.Contains(string(out), `"input_tokens":20`) {
		t.Errorf("expected real usage 20 to win over estimate 9999, got: %s", out)
	}
}

// An upstream that dies after the finish chunk (no [DONE], no usage) is still
// closed by EnsureStreamClosed, exactly once.
func TestClaudeStreamTerminalEnsureClosedAfterDeferredFinish(t *testing.T) {
	const key = "terminal-ensure-closed"
	defer ClearStreamState(key)

	out := streamClaude(t, key,
		`{"id":"`+key+`","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`,
		fmt.Sprintf(finishChunkFmt, key),
	)
	if got := countTerminalDeltas(out); got != 0 {
		t.Errorf("finish without usage must defer the terminal, got %d delta(s): %s", got, out)
	}

	closed := EnsureStreamClosed(key)
	if !strings.Contains(string(closed), `"type":"message_delta"`) || !strings.Contains(string(closed), `"type":"message_stop"`) {
		t.Errorf("expected deferred terminal from EnsureStreamClosed, got: %s", closed)
	}
	if again := EnsureStreamClosed(key); again != nil {
		t.Errorf("second EnsureStreamClosed must be a no-op, got: %s", again)
	}
}

// The non-streaming path splits the cache-inclusive OpenAI prompt count the
// same way the streaming terminal does.
func TestTranslateOpenAIToClaudeUsageCacheSplit(t *testing.T) {
	input := []byte(`{"id":"chatcmpl-cache","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":40}}}`)
	out, _, err := TranslateOpenAIToClaude(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, `"input_tokens":60`) {
		t.Errorf("expected input_tokens 60 (100 - 40 cached), got: %s", s)
	}
	if !strings.Contains(s, `"cache_read_input_tokens":40`) {
		t.Errorf("expected cache_read_input_tokens 40, got: %s", s)
	}
}
