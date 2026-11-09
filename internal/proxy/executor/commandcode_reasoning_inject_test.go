package executor

import (
	json "encoding/json/v2"
	"testing"
)

func TestInjectReasoningContent_CommandCodeLane(t *testing.T) {
	t.Run("commandcode with non-deepseek model (zai-org/GLM-5.2) gets reasoning_content", func(t *testing.T) {
		input := []byte(`{
			"model": "zai-org/GLM-5.2",
			"messages": [
				{"role": "user", "content": "hello"},
				{"role": "assistant", "content": "hi"}
			]
		}`)
		res := InjectReasoningContent(input, "commandcode")
		var reqMap map[string]any
		if err := json.Unmarshal(res, &reqMap); err != nil {
			t.Fatalf("failed to parse output: %v", err)
		}
		msgs := reqMap["messages"].([]any)
		asst := msgs[1].(map[string]any)
		if rc, ok := asst["reasoning_content"].(string); !ok || rc != " " {
			t.Errorf("expected reasoning_content ' ', got %v", asst["reasoning_content"])
		}
	})

	t.Run("commandcode with kimi model (moonshotai/Kimi-K3.1-Pro) gets reasoning_content (provider rule wins over model tool_calls rule)", func(t *testing.T) {
		input := []byte(`{
			"model": "moonshotai/Kimi-K3.1-Pro",
			"messages": [
				{"role": "user", "content": "hello"},
				{"role": "assistant", "content": "hi"}
			]
		}`)
		res := InjectReasoningContent(input, "commandcode")
		var reqMap map[string]any
		if err := json.Unmarshal(res, &reqMap); err != nil {
			t.Fatalf("failed to parse output: %v", err)
		}
		msgs := reqMap["messages"].([]any)
		asst := msgs[1].(map[string]any)
		if rc, ok := asst["reasoning_content"].(string); !ok || rc != " " {
			t.Errorf("expected reasoning_content ' ' due to commandcode provider rule, got %v", asst["reasoning_content"])
		}
	})

	t.Run("commandcode preserves client-sent reasoning_content verbatim", func(t *testing.T) {
		input := []byte(`{
			"model": "zai-org/GLM-5.2",
			"messages": [
				{"role": "user", "content": "hello"},
				{"role": "assistant", "content": "hi", "reasoning_content": "I reasoned"}
			]
		}`)
		res := InjectReasoningContent(input, "commandcode")
		var reqMap map[string]any
		if err := json.Unmarshal(res, &reqMap); err != nil {
			t.Fatalf("failed to parse output: %v", err)
		}
		msgs := reqMap["messages"].([]any)
		asst := msgs[1].(map[string]any)
		if rc, ok := asst["reasoning_content"].(string); !ok || rc != "I reasoned" {
			t.Errorf("expected 'I reasoned', got %v", asst["reasoning_content"])
		}
	})

	t.Run("user and tool turns never gain reasoning_content", func(t *testing.T) {
		input := []byte(`{
			"model": "zai-org/GLM-5.2",
			"messages": [
				{"role": "user", "content": "hello"},
				{"role": "tool", "content": "result"}
			]
		}`)
		res := InjectReasoningContent(input, "commandcode")
		var reqMap map[string]any
		if err := json.Unmarshal(res, &reqMap); err != nil {
			t.Fatalf("failed to parse output: %v", err)
		}
		msgs := reqMap["messages"].([]any)
		for _, m := range msgs {
			mm := m.(map[string]any)
			if _, exists := mm["reasoning_content"]; exists {
				t.Errorf("unexpected reasoning_content on non-assistant turn: %#v", mm)
			}
		}
	})

	t.Run("openrouter provider with same body is byte-identical", func(t *testing.T) {
		input := []byte(`{
			"model": "zai-org/GLM-5.2",
			"messages": [
				{"role": "user", "content": "hello"},
				{"role": "assistant", "content": "hi"}
			]
		}`)
		res := InjectReasoningContent(input, "openrouter")
		if string(res) != string(input) {
			t.Errorf("expected byte-identical output for openrouter, got %s", string(res))
		}
	})
}
