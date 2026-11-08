package providers

import (
	"slices"
	"testing"
)

// AI Studio rejects a thinkingLevel of "medium" on the Omni family with
// 400 "protocol error 3: 'medium' is not a supported thinking level for this
// model. Allowed values are: high, low." Two gaps let that through: no
// *gemini*omni* capability row (so the generic *gemini* fallback set
// Reasoning:false and GetThinkingLevels returned nil) and no patternThinking
// entry (so the gemini-level default {minimal,low,medium,high} applied).
//
// The declared set is HIGH alone, not {"high","low"} — see the ordering note on
// the patternThinking entry. TestGeminiOmniWireLevel in internal/thinking pins
// the wire consequence on both lanes.
func TestGeminiOmniThinkingLevels(t *testing.T) {
	InvalidateCapabilitiesCache()
	t.Cleanup(InvalidateCapabilitiesCache)

	omniIDs := []string{
		"gemini-omni-1.2-flash",
		"gemini-omni-1.2-pro",
		"gemini-omni",
		"models/gemini-omni-1.2-flash",
	}

	t.Run("caps: reasoning on the gemini-level wire, thinking always on", func(t *testing.T) {
		for _, model := range omniIDs {
			caps := GetCapabilitiesForModel("gemini", model)
			if !caps.Reasoning {
				t.Errorf("%s: Reasoning = false, want true", model)
			}
			if caps.ThinkingFormat != "gemini-level" {
				t.Errorf("%s: ThinkingFormat = %q, want gemini-level", model, caps.ThinkingFormat)
			}
			if caps.ThinkingCanDisable == nil || *caps.ThinkingCanDisable {
				t.Errorf("%s: ThinkingCanDisable = %v, want false", model, caps.ThinkingCanDisable)
			}
		}
	})

	t.Run("the advertised set is high alone", func(t *testing.T) {
		want := []string{"high"}
		for _, model := range omniIDs {
			got := GetThinkingLevels("gemini", model)
			if !slices.Equal(got, want) {
				t.Errorf("GetThinkingLevels(gemini, %q) = %v, want %v", model, got, want)
			}
		}
	})

	t.Run("medium and minimal are never advertised", func(t *testing.T) {
		for _, model := range omniIDs {
			for _, bad := range []string{"medium", "minimal"} {
				if slices.Contains(GetThinkingLevels("gemini", model), bad) {
					t.Errorf("%s advertises %q, which AI Studio answers 400 for", model, bad)
				}
			}
		}
	})

	t.Run("every provider that can carry the id resolves the same set", func(t *testing.T) {
		want := []string{"high"}
		for _, provider := range []string{"gemini", "vertex", "gemini-cli", "antigravity", "ag", "openai-compatible-chat-studio", ""} {
			got := GetThinkingLevels(provider, "gemini-omni-1.2-flash")
			if !slices.Equal(got, want) {
				t.Errorf("GetThinkingLevels(%q, gemini-omni-1.2-flash) = %v, want %v", provider, got, want)
			}
		}
	})
}

// patternThinking and patternCapabilities are both first-match-wins, so the
// omni rows sit above the numbered gemini rows. This pins the inverse: the omni
// pattern must not swallow a numbered model that merely shares the prefix.
func TestGeminiOmniPatternDoesNotClaimNumberedModels(t *testing.T) {
	InvalidateCapabilitiesCache()
	t.Cleanup(InvalidateCapabilitiesCache)

	tests := []struct {
		model string
		want  []string
	}{
		{model: "gemini-4-argon", want: []string{"low", "medium", "high"}},
		{model: "gemini-3.8-flash", want: []string{"low", "medium", "high"}},
		{model: "gemini-3.7-flash", want: []string{"low", "medium", "high"}},
		{model: "gemini-3.1-pro-preview", want: []string{"minimal", "low", "medium", "high"}},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got := GetThinkingLevels("gemini", tt.model)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("GetThinkingLevels(gemini, %q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}

	t.Run("a numbered omni id still resolves to the omni set", func(t *testing.T) {
		got := GetThinkingLevels("gemini", "gemini-4-omni-flash")
		want := []string{"high"}
		if !slices.Equal(got, want) {
			t.Fatalf("GetThinkingLevels(gemini, gemini-4-omni-flash) = %v, want %v", got, want)
		}
	})
}
