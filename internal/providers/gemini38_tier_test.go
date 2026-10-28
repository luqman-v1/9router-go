package providers

import (
	"slices"
	"testing"
)

// Gemini 3.8 tiered ids (gemini-3.8-flash-low/medium/high, the antigravity
// lane's tier-in-model-id shape) answer 400 "Thinking level MINIMAL is not
// supported for this model" to a thinkingLevel minimal — probed live through
// the agy-low combo. LOW is the floor, same as Gemini 4 Argon, so the pattern
// table owns a gemini-3.8 entry and every level the wire could carry clamps
// onto the advertised set.
func TestGemini38TierThinkingLevels(t *testing.T) {
	InvalidateCapabilitiesCache()
	t.Cleanup(InvalidateCapabilitiesCache)

	want := []string{"low", "medium", "high"}
	models := []string{
		"gemini-3.8-flash",
		"gemini-3.8-flash-low",
		"gemini-3.8-flash-medium",
		"gemini-3.8-flash-high",
	}
	providers := []string{"", "antigravity", "ag"}

	for _, p := range providers {
		for _, m := range models {
			if got := GetThinkingLevels(p, m); !slices.Equal(got, want) {
				t.Errorf("GetThinkingLevels(%q, %q) = %v, want %v", p, m, got, want)
			}
		}
	}
}

// TestGemini38TierClamp pins the level the wire carries for every input a
// client may send: a disable ask (none/minimal) lands on low, and every
// advertised level passes through untouched. No input may produce minimal.
func TestGemini38TierClamp(t *testing.T) {
	InvalidateCapabilitiesCache()
	t.Cleanup(InvalidateCapabilitiesCache)

	tests := []struct {
		level string
		want  string
	}{
		// "none" is not tested here: it is normalised to ModeNone→minimal in
		// the thinking package before any clamp runs (see the translator test
		// for the wire-level none→low guarantee), and ClampGeminiLevel is a
		// port-surface helper with no production caller.
		{level: "minimal", want: "low"},
		{level: "low", want: "low"},
		{level: "medium", want: "medium"},
		{level: "high", want: "high"},
		{level: "xhigh", want: "high"},
		{level: "max", want: "high"},
	}

	for _, m := range []string{"gemini-3.8-flash", "gemini-3.8-flash-low", "gemini-3.8-flash-medium", "gemini-3.8-flash-high"} {
		for _, tt := range tests {
			if got := ClampGeminiLevel("antigravity", m, tt.level); got != tt.want {
				t.Errorf("ClampGeminiLevel(antigravity, %q, %q) = %q, want %q", m, tt.level, got, tt.want)
			}
		}
	}
}
