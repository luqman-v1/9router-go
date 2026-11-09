package translator

import (
	json "encoding/json/v2"
	"testing"
)

// Anthropic rejects a prefill whose final text ends in whitespace ("final
// assistant content cannot end with trailing whitespace"), and token
// compression can leave one behind. The normalization trims the tail only:
// whitespace inside the caller's text belongs to the caller.

func textOfTail(t *testing.T, messages []any) []string {
	t.Helper()
	if len(messages) == 0 {
		t.Fatal("messages is empty")
	}
	tail, ok := messages[len(messages)-1].(map[string]any)
	if !ok {
		t.Fatalf("tail = %v, want an object", messages[len(messages)-1])
	}
	content, ok := tail["content"].([]any)
	if !ok {
		t.Fatalf("tail content = %v, want a block array", tail["content"])
	}
	var texts []string
	for _, bRaw := range content {
		if block, ok := bRaw.(map[string]any); ok {
			if text, ok := block["text"].(string); ok && block["type"] == "text" {
				texts = append(texts, text)
			}
		}
	}
	return texts
}

func TestNormalizeClaudeFinalAssistantWhitespace(t *testing.T) {
	tests := []struct {
		name        string
		messages    []any
		wantChanged bool
		wantTexts   []string
	}{
		{
			name:        "trailing whitespace trimmed and internal whitespace kept",
			messages:    []any{claudeTurn("user", textBlock("hi")), claudeTurn("assistant", textBlock("line one   line two \n\t"))},
			wantChanged: true,
			wantTexts:   []string{"line one   line two"},
		},
		{
			name:        "whitespace-only trailing text block removed",
			messages:    []any{claudeTurn("user", textBlock("hi")), claudeTurn("assistant", textBlock("kept"), textBlock("  \n\t"))},
			wantChanged: true,
			wantTexts:   []string{"kept"},
		},
		{
			name:        "removal exposes another trailing text block to trim",
			messages:    []any{claudeTurn("user", textBlock("hi")), claudeTurn("assistant", textBlock("kept  "), textBlock("   "))},
			wantChanged: true,
			wantTexts:   []string{"kept"},
		},
		{
			name:        "final user turn is left alone",
			messages:    []any{claudeTurn("assistant", textBlock("answer")), claudeTurn("user", textBlock("next  "))},
			wantChanged: false,
			wantTexts:   []string{"next  "},
		},
		{
			name:        "assistant turn that is not final is left alone",
			messages:    []any{claudeTurn("assistant", textBlock("earlier  ")), claudeTurn("user", textBlock("hi"))},
			wantChanged: false,
			wantTexts:   []string{"hi"},
		},
		{
			name: "non-text final block is left alone",
			messages: []any{
				claudeTurn("user", textBlock("hi")),
				claudeTurn("assistant", textBlock("checking  "), map[string]any{"type": "tool_use", "id": "toolu_1", "name": "Read", "input": map[string]any{}}),
			},
			wantChanged: false,
			wantTexts:   []string{"checking  "},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeClaudeFinalAssistantWhitespace(tt.messages); got != tt.wantChanged {
				t.Errorf("changed = %v, want %v", got, tt.wantChanged)
			}
			got := textOfTail(t, tt.messages)
			if len(got) != len(tt.wantTexts) {
				t.Fatalf("text blocks = %q, want %q", got, tt.wantTexts)
			}
			for i := range tt.wantTexts {
				if got[i] != tt.wantTexts[i] {
					t.Errorf("text[%d] = %q, want %q", i, got[i], tt.wantTexts[i])
				}
			}
		})
	}
}

func TestNormalizeClaudeFinalAssistantWhitespace_StringContent(t *testing.T) {
	messages := []any{
		claudeTurn("user", textBlock("hi")),
		map[string]any{"role": "assistant", "content": "line one   line two \n"},
	}
	if !NormalizeClaudeFinalAssistantWhitespace(messages) {
		t.Fatal("trailing whitespace on a string prefill must be trimmed")
	}
	got, _ := messages[len(messages)-1].(map[string]any)["content"].(string)
	if want := "line one   line two"; got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

// The prefill path runs through SanitizeClaudePassthrough, so the trim must
// survive encoding and must not trigger the trailing-user repair.
func TestSanitizeClaudePassthrough_TrimsFinalAssistantPrefillWhitespace(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","messages":[
		{"role":"user","content":[{"type":"text","text":"Continue the sentence"}]},
		{"role":"assistant","content":[{"type":"text","text":"The answer is  42  \n"}]}
	]}`)

	if !ClaudeIntentionalPrefill(body) {
		t.Fatal("a trailing assistant turn in messages[] is a prefill")
	}
	got := SanitizeClaudePassthrough(body, true)

	var decoded struct {
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("unmarshal sanitized body: %v (%s)", err, got)
	}
	if len(decoded.Messages) != 2 || decoded.Messages[1].Role != "assistant" {
		t.Fatalf("messages = %+v, want the client's assistant prefill kept", decoded.Messages)
	}
	if want := "The answer is  42"; decoded.Messages[1].Content[0].Text != want {
		t.Errorf("prefill = %q, want %q", decoded.Messages[1].Content[0].Text, want)
	}
	if containsPlaceholder(got) {
		t.Errorf("a client prefill must not get a placeholder appended: %s", got)
	}
}

// An empty trailing text block carried by a prefill is removed rather than
// left for the provider to reject.
func TestSanitizeClaudePassthrough_RemovesEmptyFinalTextBlock(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","messages":[
		{"role":"user","content":[{"type":"text","text":"hi"}]},
		{"role":"assistant","content":[{"type":"text","text":"kept"},{"type":"text","text":"   "}]}
	]}`)

	got := SanitizeClaudePassthrough(body, true)

	var decoded struct {
		Messages []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("unmarshal sanitized body: %v (%s)", err, got)
	}
	tail := decoded.Messages[len(decoded.Messages)-1].Content
	if len(tail) != 1 || tail[0].Text != "kept" {
		t.Errorf("tail content = %+v, want only the kept text block", tail)
	}
}
