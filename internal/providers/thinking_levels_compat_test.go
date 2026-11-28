package providers

import (
	"slices"
	"testing"
)

// TestGetThinkingLevels_OpenAICompatGPT56Max pins the compat-node rule that
// exposes "max" for GPT-5.6 / GPT-6 on openai-compatible-* nodes, while the
// built-in openai lane and gpt-5 stay clamped to xhigh.
func TestGetThinkingLevels_OpenAICompatGPT56Max(t *testing.T) {
	cases := []struct {
		model string
		max   bool
	}{
		{"gpt-6-astra", true},
		{"gpt-6.1-sol", true},
		{"gpt-6-luna", true},
		{"gpt-5.6-sol", true},
		{"gpt-5.6-terra", true},
		{"gpt-5.6-luna", true},
		{"gpt-5", false},
	}
	for _, c := range cases {
		t.Run(c.model, func(t *testing.T) {
			InvalidateCapabilitiesCache()
			got := GetThinkingLevels("openai-compatible-chat-test", c.model)
			hasMax := slices.Contains(got, "max")
			if hasMax != c.max {
				t.Errorf("GetThinkingLevels(openai-compatible-chat-test, %q) = %v, hasMax=%v want %v",
					c.model, got, hasMax, c.max)
			}
		})
	}

	// Built-in openai lane never advertises "max" for these ids.
	t.Run("openai built-in no max", func(t *testing.T) {
		InvalidateCapabilitiesCache()
		for _, m := range []string{"gpt-6-astra", "gpt-5.6-sol"} {
			got := GetThinkingLevels("openai", m)
			if slices.Contains(got, "max") {
				t.Errorf("GetThinkingLevels(openai, %q) = %v, must not include max", m, got)
			}
		}
	})
}
