package executor

import (
	json "encoding/json/v2"
	"strings"
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
			input:    "thinking...",
			expected: "",
		},
		{
			name:     "thinking with trailing answer",
			input:    "thought process</think>Here is the final code.",
			expected: "Here is the final code.",
		},
		{
			name:     "multiple think tags takes text after last closing tag",
			input:    "part1</think>intermediate part2</think>   Final answer",
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

func TestComposerThinkingDecoder_StatefulAcrossChunks(t *testing.T) {
	decoder := &composerThinkingDecoder{}
	chunks := [][]byte{
		[]byte(`{"model":"composer-1","choices":[{"delta":{"thinking":"Let me think about this"}}]}`),
		[]byte(`{"model":"composer-1","choices":[{"delta":{"thinking":"</think>Here is the"}}]}`),
		[]byte(`{"model":"composer-1","choices":[{"delta":{"thinking":" visible answer"}}]}`),
	}
	expected := "Here is the visible answer"
	var got strings.Builder
	for _, chunk := range chunks {
		out := decoder.Process(chunk)
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		choices, _ := m["choices"].([]any)
		delta, _ := choices[0].(map[string]any)["delta"].(map[string]any)
		if c, ok := delta["content"].(string); ok {
			got.WriteString(c)
		}
	}
	if got.String() != expected {
		t.Errorf("got %q, want %q", got.String(), expected)
	}
}

func TestComposerThinkingDecoder_TagSplitAcrossChunkBoundary(t *testing.T) {
	decoder := &composerThinkingDecoder{}
	chunk1 := []byte(`{"model":"composer-1","choices":[{"delta":{"thinking":"` + `deep thought...</th` + `"}}]}`)
	chunk2 := []byte(`{"model":"composer-1","choices":[{"delta":{"thinking":"ink>the answer"}}]}`)
	got := ""
	for _, chunk := range [][]byte{chunk1, chunk2} {
		out := decoder.Process(chunk)
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		choices, _ := m["choices"].([]any)
		delta, _ := choices[0].(map[string]any)["delta"].(map[string]any)
		if c, ok := delta["content"].(string); ok {
			got += c
		}
	}
	if got != "the answer" {
		t.Errorf("got %q, want %q", got, "the answer")
	}
}

func TestComposerThinkingDecoder_ReasoningContentFallback(t *testing.T) {
	decoder := &composerThinkingDecoder{}
	chunk := []byte(`{"model":"composer-1","choices":[{"delta":{"reasoning_content":"private thinking</think>visible result"}}]}`)
	out := decoder.Process(chunk)
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	choices, _ := m["choices"].([]any)
	delta, _ := choices[0].(map[string]any)["delta"].(map[string]any)
	if delta["content"] != "visible result" {
		t.Errorf("expected content from reasoning_content fallback, got %v", delta["content"])
	}
}

func TestComposerThinkingDecoder_AlreadyPopulatedContentMerged(t *testing.T) {
	decoder := &composerThinkingDecoder{}
	chunk := []byte(`{"model":"composer-1","choices":[{"delta":{"content":"already there","thinking":"thinking</think>visible"}}]}`)
	out := decoder.Process(chunk)
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	choices, _ := m["choices"].([]any)
	delta, _ := choices[0].(map[string]any)["delta"].(map[string]any)
	if delta["content"] != "already therevisible" {
		t.Errorf("expected merged content, got %v", delta["content"])
	}
}

func TestComposerThinkingDecoder_NonComposerModelUntouched(t *testing.T) {
	payload := []byte(`{"model":"gpt-4o","choices":[{"delta":{"thinking":"thinking</think>visible"}}]}`)
	decoder := &composerThinkingDecoder{}
	got := decoder.Process(payload)
	if string(got) != string(payload) {
		t.Errorf("non-composer model should not be modified, got %s", string(got))
	}
}

func TestComposerThinkingDecoder_InvalidJSONPassthrough(t *testing.T) {
	payload := []byte(`not json`)
	decoder := &composerThinkingDecoder{}
	got := decoder.Process(payload)
	if string(got) != string(payload) {
		t.Errorf("invalid json should pass through, got %s", string(got))
	}
}
