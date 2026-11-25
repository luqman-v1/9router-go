package providers

import (
	"testing"
)

func TestGetOpenDesignLane(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  string
	}{
		{"claude-haiku-5.5 → responses", "claude-haiku-5.5", FormatOpenAIResponses},
		{"deepseek-v4-pro → responses", "deepseek-v4-pro", FormatOpenAIResponses},
		{"glm-5.3-flash → claude messages", "glm-5.3-flash", FormatClaude},
		{"deepseek-v4-flash → chat", "deepseek-v4-flash", FormatOpenAI},
		{"kimi-k2.7-code → chat", "kimi-k2.7-code", FormatOpenAI},
		{"unknown id → chat", "some-new-model", FormatOpenAI},
		{"empty → chat", "", FormatOpenAI},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetOpenDesignLane(tt.model)
			if got != tt.want {
				t.Errorf("GetOpenDesignLane(%q) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}

func TestOpenDesignAlias(t *testing.T) {
	got := ResolveAlias("od")
	if got != "opendesign" {
		t.Errorf("ResolveAlias(od) = %q, want opendesign", got)
	}
}
