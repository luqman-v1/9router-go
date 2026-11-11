package translator

import (
	json "encoding/json/v2"
	"testing"
)

func TestFilterToOpenAIFormatFlattenContent(t *testing.T) {
	newBody := func() map[string]any {
		return map[string]any{
			"messages": []any{
				map[string]any{
					"role": "user",
					"content": []any{
						map[string]any{"type": "text", "text": "Hello "},
						map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.com/pic.png"}},
						map[string]any{"type": "text", "text": "world!"},
					},
				},
				map[string]any{
					"role": "assistant",
					"content": []any{
						map[string]any{"type": "text", "text": "Answer part 1. "},
						map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": "audio"}},
						map[string]any{"type": "text", "text": "Answer part 2."},
					},
				},
			},
		}
	}

	t.Run("FlattenContent true replaces array with concatenated text blocks in order", func(t *testing.T) {
		out := filterBodyWith(t, newBody(), FilterOptions{FlattenContent: true})
		msgs, ok := out["messages"].([]any)
		if !ok || len(msgs) != 2 {
			t.Fatalf("expected 2 messages, got %+v", out["messages"])
		}

		userMsg := msgs[0].(map[string]any)
		userContent, ok := userMsg["content"].(string)
		if !ok {
			t.Fatalf("expected user content to be plain string, got %T: %+v", userMsg["content"], userMsg["content"])
		}
		if want := "Hello world!"; userContent != want {
			t.Errorf("user content = %q, want %q", userContent, want)
		}

		asstMsg := msgs[1].(map[string]any)
		asstContent, ok := asstMsg["content"].(string)
		if !ok {
			t.Fatalf("expected assistant content to be plain string, got %T: %+v", asstMsg["content"], asstMsg["content"])
		}
		if want := "Answer part 1. Answer part 2."; asstContent != want {
			t.Errorf("assistant content = %q, want %q", asstContent, want)
		}
	})

	t.Run("FlattenContent false default leaves array content untouched", func(t *testing.T) {
		out := filterBodyWith(t, newBody(), FilterOptions{FlattenContent: false})
		msgs, ok := out["messages"].([]any)
		if !ok || len(msgs) != 2 {
			t.Fatalf("expected 2 messages, got %+v", out["messages"])
		}

		userMsg := msgs[0].(map[string]any)
		userParts, ok := userMsg["content"].([]any)
		if !ok {
			t.Fatalf("expected user content to remain array, got %T: %+v", userMsg["content"], userMsg["content"])
		}
		if len(userParts) != 3 {
			t.Fatalf("expected 3 parts in user content, got %d", len(userParts))
		}

		asstMsg := msgs[1].(map[string]any)
		asstParts, ok := asstMsg["content"].([]any)
		if !ok {
			t.Fatalf("expected assistant content to remain array, got %T: %+v", asstMsg["content"], asstMsg["content"])
		}
		if len(asstParts) != 3 {
			t.Fatalf("expected 3 parts in assistant content, got %d", len(asstParts))
		}
	})

	t.Run("FilterToOpenAIFormatJSON flattens content array", func(t *testing.T) {
		raw := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"foo "},{"type":"image_url","image_url":{"url":"x"}},{"type":"text","text":"bar"}]}]}`)
		out, err := FilterToOpenAIFormatJSON(raw, FilterOptions{FlattenContent: true})
		if err != nil {
			t.Fatalf("FilterToOpenAIFormatJSON: %v", err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(out, &parsed); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		msgs := parsed["messages"].([]any)
		content, ok := msgs[0].(map[string]any)["content"].(string)
		if !ok || content != "foo bar" {
			t.Errorf("content = %v, want %q", msgs[0].(map[string]any)["content"], "foo bar")
		}
	})
}
