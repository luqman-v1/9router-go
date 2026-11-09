package translator

import (
	"testing"
)

func TestOpenAIReasoningAliasCollision(t *testing.T) {
	t.Run("string reasoning removed when reasoning_content present", func(t *testing.T) {
		out := filterBody(t, map[string]any{"messages": []any{
			map[string]any{
				"role":              "assistant",
				"content":           "hi",
				"reasoning":         "I am thinking",
				"reasoning_content": "I am thinking",
			},
		}})
		msg := out["messages"].([]any)[0].(map[string]any)
		if _, has := msg["reasoning"]; has {
			t.Fatalf("reasoning must be removed: %+v", msg)
		}
		if rc, _ := msg["reasoning_content"].(string); rc != "I am thinking" {
			t.Fatalf("reasoning_content must be kept: %+v", msg)
		}
	})

	t.Run("tool call assistant message keeps reasoning_content and tool_calls", func(t *testing.T) {
		out := filterBody(t, map[string]any{"messages": []any{
			map[string]any{
				"role":              "assistant",
				"reasoning":         "I am thinking",
				"reasoning_content": "I am thinking",
				"tool_calls":        []any{map[string]any{"id": "1", "type": "function"}},
			},
		}})
		msg := out["messages"].([]any)[0].(map[string]any)
		if _, has := msg["reasoning"]; has {
			t.Fatalf("reasoning must be removed: %+v", msg)
		}
		if _, has := msg["reasoning_content"]; !has {
			t.Fatalf("reasoning_content must be kept: %+v", msg)
		}
		if _, has := msg["tool_calls"]; !has {
			t.Fatalf("tool_calls must be kept: %+v", msg)
		}
	})

	t.Run("empty string reasoning_content retained", func(t *testing.T) {
		out := filterBody(t, map[string]any{"messages": []any{
			map[string]any{
				"role":              "assistant",
				"content":           "hi",
				"reasoning":         "I am thinking",
				"reasoning_content": "",
			},
		}})
		msg := out["messages"].([]any)[0].(map[string]any)
		if _, has := msg["reasoning"]; has {
			t.Fatalf("reasoning must be removed: %+v", msg)
		}
		if _, has := msg["reasoning_content"]; !has {
			t.Fatalf("empty string reasoning_content must be kept: %+v", msg)
		}
	})

	t.Run("nil reasoning_content retained", func(t *testing.T) {
		out := filterBody(t, map[string]any{"messages": []any{
			map[string]any{
				"role":              "assistant",
				"content":           "hi",
				"reasoning":         "I am thinking",
				"reasoning_content": nil,
			},
		}})
		msg := out["messages"].([]any)[0].(map[string]any)
		if _, has := msg["reasoning"]; has {
			t.Fatalf("reasoning must be removed: %+v", msg)
		}
		if _, has := msg["reasoning_content"]; !has {
			t.Fatalf("nil reasoning_content must be kept: %+v", msg)
		}
	})

	t.Run("structured reasoning map preserved with reasoning_content", func(t *testing.T) {
		out := filterBody(t, map[string]any{"messages": []any{
			map[string]any{
				"role":              "assistant",
				"content":           "hi",
				"reasoning":         map[string]any{"effort": "high"},
				"reasoning_content": "I am thinking",
			},
		}})
		msg := out["messages"].([]any)[0].(map[string]any)
		if _, has := msg["reasoning"]; !has {
			t.Fatalf("structured reasoning must be preserved: %+v", msg)
		}
		if _, has := msg["reasoning_content"]; !has {
			t.Fatalf("reasoning_content must be kept: %+v", msg)
		}
	})

	t.Run("standalone reasoning preserved without reasoning_content", func(t *testing.T) {
		out := filterBody(t, map[string]any{"messages": []any{
			map[string]any{
				"role":      "assistant",
				"content":   "hi",
				"reasoning": "I am thinking",
			},
		}})
		msg := out["messages"].([]any)[0].(map[string]any)
		if rc, _ := msg["reasoning"].(string); rc != "I am thinking" {
			t.Fatalf("standalone reasoning must be preserved: %+v", msg)
		}
	})

	t.Run("filterMessages does not mutate original slice elements", func(t *testing.T) {
		orig := map[string]any{
			"role":              "assistant",
			"content":           "hi",
			"reasoning":         "I am thinking",
			"reasoning_content": "I am thinking",
		}
		msgs := []any{orig}
		out, changed := filterMessages(msgs, FilterOptions{})
		if !changed {
			t.Fatalf("expected changed=true")
		}
		if _, has := orig["reasoning"]; !has {
			t.Fatalf("original message reasoning mutated in place: %+v", orig)
		}
		msg := out[0].(map[string]any)
		if _, has := msg["reasoning"]; has {
			t.Fatalf("output reasoning must be removed: %+v", msg)
		}
	})
}
