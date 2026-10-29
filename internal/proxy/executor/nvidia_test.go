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
			normalized := normalizeNvidiaReasoning([]byte(tc.input))

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
