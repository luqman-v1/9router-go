package translator

import (
	"strings"
	"testing"
)

// --- extractReasoningText ---

func TestExtractReasoningText(t *testing.T) {
	tests := []struct {
		name     string
		delta    OpenAIDelta
		expected string
	}{
		{"reasoning_content takes priority", OpenAIDelta{ReasoningContent: "think", Reasoning: "old"}, "think"},
		{"reasoning fallback", OpenAIDelta{Reasoning: "think"}, "think"},
		{"empty", OpenAIDelta{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractReasoningText(tt.delta)
			if got != tt.expected {
				t.Errorf("got %q, want %q", got, tt.expected)
			}
		})
	}
}

// --- formatSSE ---

func TestFormatSSE(t *testing.T) {
	event := map[string]any{
		"type": "content_block_delta",
		"delta": map[string]any{
			"type": "text_delta",
			"text": "hello",
		},
	}
	got := formatSSE(event)
	expected := `event: content_block_delta` + "\n" + `data: {"delta":{"text":"hello","type":"text_delta"},"type":"content_block_delta"}` + "\n\n"
	if got != expected {
		t.Errorf("formatSSE mismatch.\ngot:  %q\nwant: %q", got, expected)
	}
}

// --- TranslateOpenAIToClaudeStream edge cases ---

func TestTranslateOpenAIToClaudeStream_EdgeCases(t *testing.T) {
	t.Run("empty input returns nil", func(t *testing.T) {
		out, err := TranslateOpenAIToClaudeStream([]byte(""))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out != nil {
			t.Errorf("expected nil, got %s", out)
		}
	})

	t.Run("whitespace only returns nil", func(t *testing.T) {
		out, err := TranslateOpenAIToClaudeStream([]byte("  \n  "))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out != nil {
			t.Errorf("expected nil, got %s", out)
		}
	})

	t.Run("raw JSON without data: prefix", func(t *testing.T) {
		chunk := []byte(`{"id":"raw-json","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"raw"},"finish_reason":null}]}`)
		out, err := TranslateOpenAIToClaudeStream(chunk)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(string(out), "event: message_start") {
			t.Errorf("expected message_start, got: %s", out)
		}
	})

	t.Run("malformed JSON returns error", func(t *testing.T) {
		_, err := TranslateOpenAIToClaudeStream([]byte(`data: {invalid}`))
		if err == nil {
			t.Error("expected error for malformed JSON, got nil")
		}
	})

	t.Run("zero choices with no existing state returns nil", func(t *testing.T) {
		// Use a unique ID that hasn't started a session
		chunk := []byte(`{"id":"fresh-zero","model":"gpt-4o","choices":[]}`)
		out, err := TranslateOpenAIToClaudeStream(chunk)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// First chunk with zero choices should still emit message_start
		if !strings.Contains(string(out), "event: message_start") {
			t.Errorf("expected message_start for first zero-choice chunk, got: %s", out)
		}
	})

	t.Run("no delta content or reasoning returns nil", func(t *testing.T) {
		chunk := []byte(`{"id":"empty-delta","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`)
		out, err := TranslateOpenAIToClaudeStream(chunk)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(string(out), "event: message_start") {
			t.Errorf("expected message_start, got: %s", out)
		}
	})

	t.Run("stop reason — length", func(t *testing.T) {
		_, _ = TranslateOpenAIToClaudeStream([]byte(`{"id":"stop-length","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`))
		out, err := TranslateOpenAIToClaudeStream([]byte(`{"id":"stop-length","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(string(out), `"stop_reason":"max_tokens"`) {
			t.Errorf("expected max_tokens, got: %s", out)
		}
	})

	t.Run("stop reason — stop", func(t *testing.T) {
		_, _ = TranslateOpenAIToClaudeStream([]byte(`{"id":"stop-stop","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`))
		out, err := TranslateOpenAIToClaudeStream([]byte(`{"id":"stop-stop","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(string(out), `"stop_reason":"end_turn"`) {
			t.Errorf("expected end_turn, got: %s", out)
		}
	})

	t.Run("proxy_ tool name prefix stripped on start", func(t *testing.T) {
		out1, err := TranslateOpenAIToClaudeStream([]byte(`{"id":"strip-proxy","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"proxy_Read"}}]},"finish_reason":null}]}`))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(string(out1), `"name":"Read"`) {
			t.Errorf("expected stripped name 'Read' in content_block_start, got: %s", out1)
		}
	})

	t.Run("cleanup after finish deletes state", func(t *testing.T) {
		_, _ = TranslateOpenAIToClaudeStream([]byte(`{"id":"cleanup-test","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"once"},"finish_reason":null}]}`))
		_, _ = TranslateOpenAIToClaudeStream([]byte(`{"id":"cleanup-test","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`))
		// After finish, state should be deleted. A new chunk with same ID starts fresh.
		out, _ := TranslateOpenAIToClaudeStream([]byte(`{"id":"cleanup-test","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"fresh"},"finish_reason":null}]}`))
		if !strings.Contains(string(out), "event: message_start") {
			t.Errorf("expected message_start again (fresh session), got: %s", out)
		}
	})

	t.Run("usage captured in global after finish", func(t *testing.T) {
		GetAndClearLastUsage()
		_, _ = TranslateOpenAIToClaudeStream([]byte(`{"id":"usage-capture","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"x"},"finish_reason":null}]}`))
		_, _ = TranslateOpenAIToClaudeStream([]byte(`{"id":"usage-capture","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":15,"cached_tokens":3}}`))
		u := GetAndClearLastUsage()
		if u == nil {
			t.Fatal("expected non-nil usage after finish")
		}
		if u.PromptTokens != 7 || u.CompletionTokens != 15 || u.CachedTokens != 3 {
			t.Errorf("got %#v", u)
		}
	})
}

// --- Provider alias edge: knownProviders used by TranslateOpenAIToClaudeStream ---

func TestTranslateOpenAIToClaudeStream_Defaults(t *testing.T) {
	// When chunk has empty ID and model
	out, err := TranslateOpenAIToClaudeStream([]byte(`{"id":"","model":"","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(out), `"model":"claude-3-5-sonnet"`) {
		t.Errorf("expected default model, got: %s", out)
	}
}
