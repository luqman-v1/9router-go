package chat

import (
	"strings"
	"testing"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/translator"
)

// preservesOpenRouterAnthropicCacheControl gates the OpenAI filter's
// PreserveCacheControl flag so OpenRouter models served by Anthropic keep the
// cache_control breakpoints a Claude Code caller sent (upstream OmniRoute
// #19755). OpenRouter forwards the whole OpenAI body to the model provider
// named in the model id; Anthropic's own API honours the breakpoints, and the
// filter used to strip them here, costing every Anthropic-backed OpenRouter
// model its prompt cache.
func TestPreservesOpenRouterAnthropicCacheControl(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		wire     string
		want     bool
	}{
		{"openrouter anthropic model", "openrouter", "anthropic/claude-3.5-sonnet", true},
		{"openrouter anthropic model upper", "openrouter", "Anthropic/claude-sonnet-4", true},
		{"openrouter non-anthropic model", "openrouter", "meta-llama/llama-3.1-70b-instruct", false},
		{"other provider anthropic-prefixed", "alicode", "anthropic/claude-3.5-sonnet", false},
		{"openrouter bare anthropic prefix", "openrouter", "anthropic/", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := translator.PreservesOpenRouterAnthropicCacheControl(tt.provider, tt.wire); got != tt.want {
				t.Errorf("translator.PreservesOpenRouterAnthropicCacheControl(%q, %q) = %v, want %v", tt.provider, tt.wire, got, tt.want)
			}
		})
	}
}

// filterPreserveCacheControlFor runs the OpenAI filter the way
// tryForwardWithConnection does and reports whether cache_control survives on
// the first text block of the outbound body.
func filterPreserveCacheControlFor(t *testing.T, provider, model string) bool {
	t.Helper()
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}]}`)
	out, err := translator.FilterToOpenAIFormatJSON(body, translator.FilterOptions{
		PreserveCacheControl: providers.PreservesCacheControl(provider) || translator.PreservesOpenRouterAnthropicCacheControl(provider, model),
	})
	if err != nil {
		t.Fatalf("FilterToOpenAIFormatJSON: %v", err)
	}
	if !strings.Contains(string(out), "cache_control") {
		return false
	}
	return true
}

// TestOpenRouterAnthropicCacheControlEndToEnd exercises the real filter path:
// an OpenRouter anthropic/* model keeps the cache_control marker, a non-anthropic
// OpenRouter model loses it, and alicode (PreserveCacheControl registry flag)
// keeps it unchanged.
func TestOpenRouterAnthropicCacheControlEndToEnd(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		wantKeep bool
	}{
		{"openrouter anthropic model keeps cache_control", "openrouter", "anthropic/claude-3.5-sonnet", true},
		{"openrouter non-anthropic model strips cache_control", "openrouter", "meta-llama/llama-3.1-70b-instruct", false},
		{"alicode keeps cache_control", "alicode", "qwen3-coder", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterPreserveCacheControlFor(t, tt.provider, tt.model)
			if got != tt.wantKeep {
				t.Errorf("provider=%s model=%s: cache_control kept=%v, want %v", tt.provider, tt.model, got, tt.wantKeep)
			}
		})
	}
}
