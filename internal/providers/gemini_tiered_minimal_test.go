package providers

import (
	"slices"
	"testing"
)

func TestGeminiTieredMinimalFloorThinkingLevels(t *testing.T) {
	InvalidateCapabilitiesCache()
	t.Cleanup(InvalidateCapabilitiesCache)

	wantTiered := []string{"low", "medium", "high"}
	tieredModels := []string{
		"gemini-3.8-flash",
		"gemini-3.8-flash-low",
		"gemini-3.7-flash",
		"gemini-3.7-flash-low",
		"gemini-3.6-flash",
		"gemini-3.6-flash-low",
	}

	wantOriginal := []string{"minimal", "low", "medium", "high"}
	originalModels := []string{
		"gemini-3-flash-preview",
		"gemini-3.1-pro-preview",
	}

	providers := []string{"", "antigravity", "ag"}

	for _, p := range providers {
		for _, m := range tieredModels {
			if got := GetThinkingLevels(p, m); !slices.Equal(got, wantTiered) {
				t.Errorf("GetThinkingLevels(%q, %q) = %v, want %v", p, m, got, wantTiered)
			}
		}
		for _, m := range originalModels {
			if got := GetThinkingLevels(p, m); !slices.Equal(got, wantOriginal) {
				t.Errorf("GetThinkingLevels(%q, %q) = %v, want %v", p, m, got, wantOriginal)
			}
		}
	}
}

func TestGeminiTieredMinimalClamp(t *testing.T) {
	InvalidateCapabilitiesCache()
	t.Cleanup(InvalidateCapabilitiesCache)

	tests := []struct {
		level string
		want  string
	}{
		{level: "minimal", want: "low"},
		{level: "low", want: "low"},
		{level: "medium", want: "medium"},
		{level: "high", want: "high"},
		{level: "xhigh", want: "high"},
		{level: "max", want: "high"},
	}

	tieredModels := []string{
		"gemini-3.8-flash",
		"gemini-3.7-flash",
		"gemini-3.6-flash",
	}

	for _, m := range tieredModels {
		for _, tt := range tests {
			if got := ClampGeminiLevel("antigravity", m, tt.level); got != tt.want {
				t.Errorf("ClampGeminiLevel(antigravity, %q, %q) = %q, want %q", m, tt.level, got, tt.want)
			}
		}
	}
}
