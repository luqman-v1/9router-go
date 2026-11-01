package executor

import (
	"testing"
)

func TestBuildResponsesBody_ReasoningNormalization(t *testing.T) {
	t.Run("top-level reasoning_effort merges into reasoning and preserves summary", func(t *testing.T) {
		raw := []byte(`{
			"model": "claude-opus-5.5",
			"messages": [{"role": "user", "content": "hi"}],
			"reasoning_effort": "high",
			"reasoning": {"summary": "detailed"}
		}`)

		transformed, _, err := buildResponsesBody(raw)
		if err != nil {
			t.Fatalf("buildResponsesBody failed: %v", err)
		}

		parsed := decodeBody(t, transformed)
		reasoning, ok := parsed["reasoning"].(map[string]any)
		if !ok {
			t.Fatalf("reasoning field missing or not a map: %v", parsed["reasoning"])
		}

		if reasoning["effort"] != "high" {
			t.Errorf("reasoning.effort = %v, want 'high'", reasoning["effort"])
		}
		if reasoning["summary"] != "detailed" {
			t.Errorf("reasoning.summary = %v, want 'detailed'", reasoning["summary"])
		}
	})

	t.Run("top-level reasoning_effort overrides nested reasoning.effort", func(t *testing.T) {
		raw := []byte(`{
			"model": "claude-opus-5.5",
			"messages": [{"role": "user", "content": "hi"}],
			"reasoning_effort": "low",
			"reasoning": {"effort": "high", "summary": "auto"}
		}`)

		transformed, _, err := buildResponsesBody(raw)
		if err != nil {
			t.Fatalf("buildResponsesBody failed: %v", err)
		}

		parsed := decodeBody(t, transformed)
		reasoning, ok := parsed["reasoning"].(map[string]any)
		if !ok {
			t.Fatalf("reasoning field missing or not a map: %v", parsed["reasoning"])
		}

		if reasoning["effort"] != "low" {
			t.Errorf("reasoning.effort = %v, want 'low' (overridden by reasoning_effort)", reasoning["effort"])
		}
	})

	t.Run("without reasoning_effort, existing reasoning map is kept as-is", func(t *testing.T) {
		raw := []byte(`{
			"model": "claude-opus-5.5",
			"messages": [{"role": "user", "content": "hi"}],
			"reasoning": {"effort": "medium", "summary": "auto"}
		}`)

		transformed, _, err := buildResponsesBody(raw)
		if err != nil {
			t.Fatalf("buildResponsesBody failed: %v", err)
		}

		parsed := decodeBody(t, transformed)
		reasoning, ok := parsed["reasoning"].(map[string]any)
		if !ok {
			t.Fatalf("reasoning field missing or not a map: %v", parsed["reasoning"])
		}

		if reasoning["effort"] != "medium" {
			t.Errorf("reasoning.effort = %v, want 'medium'", reasoning["effort"])
		}
		if reasoning["summary"] != "auto" {
			t.Errorf("reasoning.summary = %v, want 'auto'", reasoning["summary"])
		}
	})

	t.Run("reasoning_effort max maps to xhigh", func(t *testing.T) {
		raw := []byte(`{
			"model": "claude-opus-5.5",
			"messages": [{"role": "user", "content": "hi"}],
			"reasoning_effort": "max"
		}`)

		transformed, _, err := buildResponsesBody(raw)
		if err != nil {
			t.Fatalf("buildResponsesBody failed: %v", err)
		}

		parsed := decodeBody(t, transformed)
		reasoning, ok := parsed["reasoning"].(map[string]any)
		if !ok {
			t.Fatalf("reasoning field missing or not a map: %v", parsed["reasoning"])
		}

		if reasoning["effort"] != "xhigh" {
			t.Errorf("reasoning.effort = %v, want 'xhigh'", reasoning["effort"])
		}
	})
}
