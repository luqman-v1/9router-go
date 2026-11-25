package providers

import "testing"

func TestExplicitToolSupport(t *testing.T) {
	tests := []struct {
		provider string
		model    string
		want     bool
	}{
		{"cloudflare-ai", "@cf/mistralai/mistral-small-3.1-24b-instruct", true},
		{"cloudflare-ai", "@cf/moonshotai/kimi-k2.6", true},
		{"cloudflare-ai", "@cf/meta/llama-3.1-8b-instruct-fp8-fast", false},
		{"cloudflare-ai", "", false},
		{"", "@cf/moonshotai/kimi-k2.6", false},
		{"nonexistent-provider", "anything", false},
	}
	for _, tt := range tests {
		if got := ExplicitToolSupport(tt.provider, tt.model); got != tt.want {
			t.Errorf("ExplicitToolSupport(%q, %q) = %v, want %v", tt.provider, tt.model, got, tt.want)
		}
	}
}
