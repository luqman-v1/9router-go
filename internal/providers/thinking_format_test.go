package providers

import (
	"reflect"
	"testing"
)

func TestThinkingFormatFor(t *testing.T) {
	for provider, want := range map[string]string{
		"tokenrouter":         "tokenrouter",
		"openrouter":          "openai",
		"siliconflow":         "openai",
		"iflow":               "openai",
		"blackbox":            "openai",
		"cloudflare-ai":       "openai",
		"codebuddy-cn":        "openai",
		"codebuddy-intl":      "openai",
		"venice":              "openai",
		"vercel-ai-gateway":   "openai",
		"minimax-code":        "claude-adaptive",
		"minimax-code-global": "claude-adaptive",
		"tokenharbor":         "",
		"openai":              "",
		"no-such-provider":    "",
	} {
		if got := ThinkingFormatFor(provider); got != want {
			t.Errorf("ThinkingFormatFor(%q) = %q, want %q", provider, got, want)
		}
	}
}

func TestModelStrip(t *testing.T) {
	hit := []string{"image", "audio"}
	for _, tc := range []struct{ provider, model string }{
		{"kiro", "deepseek-3.2"},
		{"kiro", "qwen3-coder-next"},
		{"kiro", "vendor/deepseek-3.2"},
	} {
		if got := ModelStrip(tc.provider, tc.model); !reflect.DeepEqual(got, hit) {
			t.Errorf("ModelStrip(%q, %q) = %v, want %v", tc.provider, tc.model, got, hit)
		}
	}
	for _, tc := range []struct{ provider, model string }{
		{"kiro", "other-model"},
		{"openai", "deepseek-3.2"},
		{"kr", "deepseek-3.2"},
		{"", ""},
	} {
		if got := ModelStrip(tc.provider, tc.model); got != nil {
			t.Errorf("ModelStrip(%q, %q) = %v, want nil", tc.provider, tc.model, got)
		}
	}
}
