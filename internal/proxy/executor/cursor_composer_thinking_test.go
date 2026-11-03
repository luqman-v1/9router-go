package executor

import (
	json "encoding/json/v2"
	"testing"
)

func TestVisibleComposerContentFromThinking(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty thinking",
			input:    "",
			expected: "",
		},
		{
			name:     "thinking without closing tag",
			input:    "<think>still thinking...",
			expected: "",
		},
		{
			name:     "thinking with trailing answer",
			input:    "<think>thought process</think>Here is the final code.",
			expected: "Here is the final code.",
		},
		{
			name:     "multiple think tags takes text after last closing tag",
			input:    "<think>part1</think>intermediate<think>part2</think>   Final answer",
			expected: "Final answer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := visibleComposerContentFromThinking(tt.input)
			if got != tt.expected {
				t.Errorf("got %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestDecodeComposerThinkingPayload(t *testing.T) {
	// Case 1: non-composer model leaves chunk alone
	payloadNonComposer := []byte(`{"model":"gpt-4o","choices":[{"delta":{"thinking":"<think>done</think>visible"}}]}`)
	got := decodeComposerThinkingPayload(payloadNonComposer)
	if string(got) != string(payloadNonComposer) {
		t.Errorf("non-composer model should not be modified, got %s", string(got))
	}

	// Case 2: composer model emits visible content after </think>
	payloadComposer := []byte(`{"model":"composer-1","choices":[{"delta":{"thinking":"<think>done</think>visible"}}]}`)
	gotComp := decodeComposerThinkingPayload(payloadComposer)
	var m map[string]any
	if err := json.Unmarshal(gotComp, &m); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	choices, _ := m["choices"].([]any)
	delta, _ := choices[0].(map[string]any)["delta"].(map[string]any)
	if delta["content"] != "visible" {
		t.Errorf("expected delta.content='visible', got %v", delta["content"])
	}
}
