package providers

import (
	"slices"
	"testing"
)

// #4614: patternThinking is first-match-wins, and the unqualified
// `*deepseek-v4.*` glob used to sit ABOVE the provider-qualified codebuddy-cn
// row. For a dotted id such as deepseek-v4.1-flash the generic glob matched
// first, so the codebuddy-cn row was dead code and the picker offered the
// wrong effort set. These cases pin the ordering that fixes it.
func TestGetThinkingLevels_CodeBuddyRowsBeatGenericGlob(t *testing.T) {
	InvalidateCapabilitiesCache()

	tests := []struct {
		name     string
		provider string
		model    string
		want     []string
	}{
		{
			name:     "codebuddy-cn dotted id uses its own levels",
			provider: "codebuddy-cn",
			model:    "deepseek-v4.1-flash",
			want:     []string{"low", "high", "max"},
		},
		{
			name:     "codebuddy-cn pro id uses its own levels",
			provider: "codebuddy-cn",
			model:    "deepseek-v4-pro",
			want:     []string{"low", "high", "xhigh"},
		},
		{
			name:     "other providers keep the generic set",
			provider: "commandcode",
			model:    "deepseek/deepseek-v4-pro",
			want:     []string{"none", "low", "medium", "high", "xhigh", "max"},
		},
		{
			name:     "codebuddy-cn new preview id is reachable",
			provider: "codebuddy-cn",
			model:    "kimi-k2.8-preview",
			want:     []string{"low", "medium", "high"},
		},
		{
			name:     "hy3 is an exact match, not a prefix",
			provider: "codebuddy-cn",
			model:    "hy3",
			want:     []string{"low", "high"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetThinkingLevels(tt.provider, tt.model); !slices.Equal(got, tt.want) {
				t.Errorf("GetThinkingLevels(%q, %q) = %v, want %v", tt.provider, tt.model, got, tt.want)
			}
		})
	}
}
