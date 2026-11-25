package providers

import "testing"

// Every catalogued free model must publish its real context window, not the
// substring table's floor. The free tier is what a keyless operator runs, so a
// wrong window there is the most visible version of the nous/qwen4-max bug.
// Sources: the provider's own /v1/models feed (nous, probed live), models.dev's
// per-gateway entries (opencode free lane, cloudflare-workers-ai, nvidia), and
// the upstream-synced family tables for the rest.
func TestFreeModelContextWindows(t *testing.T) {
	tests := []struct {
		provider string
		model    string
		wantCW   int
		wantMax  int
	}{
		// Nous free tier — the provider's live feed declarations.
		{"nous", "meituan/longcat-2.0:free", 1048756, 262144},
		{"nous", "meituan/longcat-2.5-preview:free", 1048576, 131072},
		{"nous", "stepfun/step-5-preview:free", 1000000, 64000},
		{"nous", "stepfun/step-3.7-flash:free", 262144, 256000},
		{"nous", "upstage/solar-mini4:free", 524288, 131072},
		{"nous", "poolside/laguna-s-2.1:free", 262144, 131072},
		{"nous", "poolside/laguna-xs-2.1:free", 262144, 32768},
		// OpenCode free lane — models.dev per-gateway figures; the ling and
		// nemotron ids used to fall to the *ling-*/*nemotron* 128k floor.
		{"opencode", "ling-3.0-flash-fin-free", 262144, 32768},
		{"opencode", "nemotron-3-ultra-free", 1000000, 128000},
		{"opencode", "nemotron-3.5-lightning-free", 262144, 262144},
		{"opencode", "big-pickle", 200000, 32000},
		{"opencode", "space-bunny-free", 1048576, 524288},
		{"opencode", "muse-spark-1.3-contributor-free", 1048576, 131072},
		{"opencode", "mimo-v2.6-flash-free", 1048576, 131072},
		// Cloudflare Workers AI truncates far below the vendor API window;
		// the family patterns used to publish the vendor number (1M for a
		// 32k qwen-coder endpoint) or the 128k floor.
		{"cloudflare-ai", "@cf/qwen/qwen2.5-coder-32b-instruct", 32768, 32768},
		{"cloudflare-ai", "@cf/qwen/qwq-32b", 24000, 24000},
		{"cloudflare-ai", "@cf/deepseek-ai/deepseek-r1-distill-qwen-32b", 80000, 80000},
		{"cloudflare-ai", "@cf/meta/llama-3.3-70b-instruct-fp8-fast", 24000, 24000},
		{"cloudflare-ai", "@cf/google/gemma-4-26b-a4b-it", 256000, 16384},
		{"cloudflare-ai", "@cf/moonshotai/kimi-k2.6", 262144, 256000},
		// NVIDIA NIM free tier.
		{"nvidia", "google/gemma-4-31b-it", 256000, 16384},
		{"nvidia", "openai/gpt-oss-120b", 131072, 131072},
		// Family-table free ids (upstream-synced).
		{"glm", "glm-4.7-flash", 200000, 128000},
		{"gemini", "gemini-2.5-flash", 1048576, 65536},
		{"qoder", "qwen3.8-max-preview", 1000000, 65536},
		{"qoder", "kimi-k3", 1048576, 131072},
		{"openrouter", "qwen/qwen3-coder:free", 1000000, 8192},
		{"openrouter", "deepseek/deepseek-v4-flash:free", 1000000, 384000},
		{"antigravity", "claude-sonnet-4-6", 1000000, 128000},
		{"antigravity", "gemini-3.7-flash-high", 1048576, 65536},
		{"vertex", "claude-sonnet-4-6", 1000000, 128000},
		{"ollama", "kimi-k2.6", 262144, 262144},
	}
	for _, tt := range tests {
		t.Run(tt.provider+"/"+tt.model, func(t *testing.T) {
			InvalidateCapabilitiesCache()
			d := GetCapabilitiesDetailForModel(tt.provider, tt.model)
			if d.ContextWindow != tt.wantCW || d.MaxOutput != tt.wantMax {
				t.Errorf("limits = (%d, %d), want (%d, %d)",
					d.ContextWindow, d.MaxOutput, tt.wantCW, tt.wantMax)
			}
		})
	}
}
