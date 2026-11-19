package providers

import "testing"

// OpenCode Zen (PAYG) and Go (subscription) serve the same free ids as the
// OpenCode free lane, plus their own paid catalog. The family patterns
// mis-guessed several windows there (ling/nemotron free ids at the 128k floor,
// hy4-preview / muse-spark contributor / beam-free on the go lane). The windows
// below are the provider's own feed declarations (opencode.ai zen/go /v1/models,
// limit.context/output — probed live) and models.dev's per-gateway entries.
// maxOutput and thinking shape stay on the upstream-synced family resolution;
// only the window is pinned per lane, so the thinking-levels fixture stays green.
func TestOpenCodeZenGoWindows(t *testing.T) {
	tests := []struct {
		provider string
		model    string
		wantCW   int
	}{
		// Zen free ids — same windows as the free lane.
		{"opencode-zen", "ling-3.0-flash-fin-free", 262144},
		{"opencode-zen", "nemotron-3-ultra-free", 1000000},
		{"opencode-zen", "nemotron-3.5-lightning-free", 262144},
		{"opencode-zen", "muse-spark-1.3-contributor-free", 1048576},
		{"opencode-zen", "mimo-v2.6-flash-free", 1048576},
		{"opencode-zen", "hy4-flash-free", 1048576},
		// Zen paid catalog — upstream-synced family windows.
		{"opencode-zen", "claude-fable-5", 1000000},
		{"opencode-zen", "claude-opus-5", 1000000},
		{"opencode-zen", "gemini-3.8-flash", 1048576},
		{"opencode-zen", "gpt-6-astra", 1050000},
		// Go lane — per-gateway windows the patterns mis-guess.
		{"opencode-go", "hy4-preview", 1024000},
		{"opencode-go", "hy4", 1048576},
		{"opencode-go", "hy4-flash-free", 1048576},
		{"opencode-go", "hy3", 256000},
		{"opencode-go", "muse-spark-1.3-contributor", 1048576},
		{"opencode-go", "muse-spark-1.2-contributor", 1048576},
		{"opencode-go", "beam-free", 1048576},
		// Go lane — upstream-synced family windows.
		{"opencode-go", "glm-5.3-flash", 1000000},
		{"opencode-go", "kimi-k3", 1048576},
		{"opencode-go", "qwen3.8-max", 1000000},
		{"opencode-go", "space-bunny-free", 1048576},
		{"opencode-go", "claude-haiku-5-5", 1000000},
		// Short aliases resolve the same tables.
		{"ocz", "ling-3.0-flash-fin-free", 262144},
		{"ocg", "hy4-preview", 1024000},
		{"oc", "hy4-flash-free", 1048576},
		{"ocz", "hy4-flash-free", 1048576},
		{"ocg", "hy4", 1048576},
		{"ocg", "hy4-flash-free", 1048576},
	}
	for _, tt := range tests {
		t.Run(tt.provider+"/"+tt.model, func(t *testing.T) {
			InvalidateCapabilitiesCache()
			d := GetCapabilitiesDetailForModel(tt.provider, tt.model)
			if d.ContextWindow != tt.wantCW {
				t.Errorf("contextWindow = %d, want %d (maxOutput=%d)",
					d.ContextWindow, tt.wantCW, d.MaxOutput)
			}
		})
	}
}
