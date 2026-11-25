package executor

import (
	json "encoding/json/v2"
	"testing"
)

func TestNormalizeNvidiaReasoning(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []map[string]any
	}{
		{
			name: "both fields present - keeps canonical reasoning_content, drops reasoning alias",
			input: `{
				"messages": [
					{"role": "assistant", "content": "", "reasoning": "alias", "reasoning_content": "canonical"}
				]
			}`,
			expected: []map[string]any{
				{"role": "assistant", "content": "", "reasoning_content": "canonical"},
			},
		},
		{
			name: "alias only - normalizes reasoning to reasoning_content",
			input: `{
				"messages": [
					{"role": "assistant", "content": "", "reasoning": "alias only"}
				]
			}`,
			expected: []map[string]any{
				{"role": "assistant", "content": "", "reasoning_content": "alias only"},
			},
		},
		{
			name: "canonical only - untouched",
			input: `{
				"messages": [
					{"role": "assistant", "content": "", "reasoning_content": "canonical only"}
				]
			}`,
			expected: []map[string]any{
				{"role": "assistant", "content": "", "reasoning_content": "canonical only"},
			},
		},
		{
			name: "empty canonical with alias - alias takes precedence",
			input: `{
				"messages": [
					{"role": "assistant", "content": "", "reasoning": "alias value", "reasoning_content": "   "}
				]
			}`,
			expected: []map[string]any{
				{"role": "assistant", "content": "", "reasoning_content": "alias value"},
			},
		},
		{
			name: "user message with reasoning - untouched",
			input: `{
				"messages": [
					{"role": "user", "content": "hello", "reasoning": "user think"}
				]
			}`,
			expected: []map[string]any{
				{"role": "user", "content": "hello", "reasoning": "user think"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			normalized := sanitizeNIMBody([]byte(tc.input))

			var res struct {
				Messages []map[string]any `json:"messages"`
			}
			if err := json.Unmarshal(normalized, &res); err != nil {
				t.Fatalf("failed to unmarshal normalized output: %v", err)
			}

			if len(res.Messages) != len(tc.expected) {
				t.Fatalf("expected %d messages, got %d", len(tc.expected), len(res.Messages))
			}

			for i, exp := range tc.expected {
				got := res.Messages[i]
				for k, v := range exp {
					if got[k] != v {
						t.Errorf("msg[%d][%q] = %v, want %v", i, k, got[k], v)
					}
				}
				if _, hasAlias := got["reasoning"]; hasAlias && exp["role"] == "assistant" {
					t.Errorf("msg[%d] should not have 'reasoning' field: %v", i, got)
				}
			}
		})
	}
}

// TestSanitizeNIMBodyStripsRejectedParams pins the nvidia rule from upstream
// paramSupport (decolua@2275bc0d): NIM answers 400 for each of these
// top-level params, so the executor drops them instead of forwarding a
// request the upstream is guaranteed to reject.
func TestSanitizeNIMBodyStripsRejectedParams(t *testing.T) {
	raw := []byte(`{
		"model": "meta/llama-3.3-70b-instruct",
		"messages": [
			{"role": "user", "content": "hello"},
			{"role": "assistant", "content": "hi", "reasoning": "thinking text"}
		],
		"client_metadata": {"source": "claude-code"},
		"prompt_cache_key": "cache-123",
		"service_tier": "default",
		"anthropic_beta": ["tools-2024"],
		"thinking_budget": 1024,
		"user_id": "user-456",
		"custom_field": "test",
		"temperature": 0.7
	}`)

	var body map[string]any
	if err := json.Unmarshal(sanitizeNIMBody(raw), &body); err != nil {
		t.Fatalf("sanitized body does not parse: %v", err)
	}

	for _, key := range nimRejectedParams {
		if _, has := body[key]; has {
			t.Errorf("%s must be stripped: %+v", key, body)
		}
	}
	if got, _ := body["temperature"].(float64); got != 0.7 {
		t.Errorf("temperature = %v, want 0.7 (params NIM accepts must survive)", body["temperature"])
	}
	if got, _ := body["model"].(string); got != "meta/llama-3.3-70b-instruct" {
		t.Errorf("model = %v, want the requested model", body["model"])
	}

	msgs, _ := body["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want 2", len(msgs))
	}
	assistant, _ := msgs[1].(map[string]any)
	if _, has := assistant["reasoning"]; has {
		t.Errorf("assistant reasoning alias must be renamed: %+v", assistant)
	}
	if got, _ := assistant["reasoning_content"].(string); got != "thinking text" {
		t.Errorf("reasoning_content = %q, want %q", got, "thinking text")
	}
}

// TestSanitizeNIMBodyLeavesCleanRequestUntouched pins the byte-for-byte
// passthrough: a request that carries none of the rejected params must not be
// re-serialised, or every NIM request would pay a marshal it does not need.
func TestSanitizeNIMBodyLeavesCleanRequestUntouched(t *testing.T) {
	cases := [][]byte{
		[]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`),
		[]byte(`not json`),
		[]byte(`{"model":"m","messages":[]}`),
	}
	for _, raw := range cases {
		if got := sanitizeNIMBody(raw); string(got) != string(raw) {
			t.Errorf("clean body changed:\n in: %s\nout: %s", raw, got)
		}
	}
}

// TestSanitizeNIMBodyKeepsCanonicalReasoning pins that an explicit
// reasoning_content wins over the alias: NIM treats the two as one field, so
// the rename must never overwrite a value the client set on purpose.
func TestSanitizeNIMBodyKeepsCanonicalReasoning(t *testing.T) {
	raw := []byte(`{"messages":[{"role":"assistant","content":"","reasoning":"alias","reasoning_content":"canonical"}]}`)
	var body struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(sanitizeNIMBody(raw), &body); err != nil {
		t.Fatalf("sanitized body does not parse: %v", err)
	}
	msg := body.Messages[0]
	if _, has := msg["reasoning"]; has {
		t.Errorf("reasoning alias must be dropped: %+v", msg)
	}
	if got, _ := msg["reasoning_content"].(string); got != "canonical" {
		t.Errorf("reasoning_content = %q, want the canonical value", got)
	}
}
