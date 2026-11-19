package providers

import "testing"

// Full-catalog sweep regression: every provider×model the gateway publishes
// must resolve its context window from an authoritative source (the provider's
// live feed, models.dev per-gateway entries, or the upstream-synced tables) —
// never the substring floor — whenever such a source exists. This pins the
// families the systematic audit found at the 128k/4096 floor.
func TestCatalogSweepNoFloorWhereSourceExists(t *testing.T) {
	tests := []struct {
		provider string
		model    string
		wantCW   int
	}{
		// gpt-oss: 131072 on every gateway (models.dev groq/cerebras/vercel).
		{"api-airforce", "gpt-oss-120b", 131072},
		{"api-airforce", "gpt-oss-20b", 131072},
		{"ollama", "gpt-oss:120b", 131072},
		// Doubao Seed 2.0 family (models.dev volcengine/byteplus).
		{"ark", "Doubao-Seed-2.0-pro", 256000},
		{"ark", "Doubao-Seed-2.0-Code", 262144},
		{"byteplus", "seed-2-0-pro", 256000},
		{"byteplus", "seed-2-0-lite-260228", 256000},
		{"sumopod", "seed-2-0-pro", 256000},
		// llama-3.3-70b / llama-3.2-3b vendor 131072 (models.dev openrouter).
		{"hyperbolic", "meta-llama/Llama-3.3-70B-Instruct", 131072},
		{"hyperbolic", "meta-llama/Llama-3.2-3B-Instruct", 131072},
		{"nebius", "meta-llama/Llama-3.3-70B-Instruct", 131072},
		{"fireworks", "accounts/fireworks/models/llama-v3p3-70b-instruct", 131072},
		{"cerebras", "llama-3.3-70b", 131072},
		{"openadapter", "meta-llama/llama-3.3-70b-instruct", 131072},
		{"kenari", "llama-3-3-70b-instruct:free", 131072},
		// nemotron-3-super-120b-a12b: 262144 (models.dev openrouter/kilo).
		{"bazaarlink", "nemotron-3-super-120b-a12b", 262144},
		{"synthetic", "hf:nvidia/NVIDIA-Nemotron-3-Super-120B-A12B-NVFP4", 262144},
		// nemotron-3.5-super-vl free / nemotron-3-ultra fp4: 1M.
		{"kenari", "nemotron-3-5-super-vl:free", 1000000},
		{"kimchi", "nemotron-3-ultra-fp4", 1000000},
		// dots-3-note-preview free: 512000 (models.dev openrouter).
		{"kenari", "dots-3-note-preview:free", 512000},
		// Cursor claude ids: vendor 4.5 = 200000, 4.6 = 1M.
		{"cursor", "claude-4.5-sonnet", 200000},
		{"cursor", "claude-4.5-opus", 200000},
		{"cursor", "claude-4.6-opus-max", 1000000},
		{"cursor", "claude-4.6-sonnet-medium-thinking", 1000000},
		// perplexity-agent claude-sonnet-4-6: vendor 1M.
		{"perplexity-agent", "anthropic/claude-sonnet-4-6", 1000000},
		// muse-spark contributor: 1M (models.dev meta/muse-spark).
		{"muse", "muse-spark-1.3-contributor", 1048576},
		{"muse", "muse-spark-1.2-contributor", 1048576},
		// hcnsec reseller windows (vendor models).
		{"hcnsec", "DeepSeek-V4-Pro", 1000000},
		{"hcnsec", "MiniMax-M3.1-Flash", 1000000},
		{"hcnsec", "MiMo-V2.6-Flash", 1048576},
		{"hcnsec", "sensenova-6.8-flash-lite", 1000000},
		// pixtral-12b dated id: 128000/128000 (models.dev mistral).
		{"mistral", "pixtral-12b-2409", 128000},
		// Cloudflare llama-3.1 fast/awq: 32000 (models.dev).
		{"cloudflare-ai", "@cf/meta/llama-3.1-8b-instruct-fp8-fast", 32000},
		{"cloudflare-ai", "@cf/meta/llama-3.1-70b-instruct-fp8-fast", 32000},
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

// The models.dev catalog reader must resolve the same keys the writer files,
// including slash-bearing ids and the togetherai/github-copilot/kilo provider
// names. These were silent misses that left real windows on the floor.
func TestCatalogLookupProviderAliases(t *testing.T) {
	if err := LoadCatalogFromFile("testdata/model-catalog.json"); err != nil {
		t.Skip("no catalog fixture")
	}
	// catalogBaseID strips the vendor namespace before the ":suffix", so
	// "hf:openai/gpt-oss-120b" normalizes to "gpt-oss-120b", not "hf".
	if got := catalogBaseID("hf:openai/gpt-oss-120b"); got != "gpt-oss-120b" {
		t.Errorf("catalogBaseID = %q, want gpt-oss-120b", got)
	}
	for _, alias := range []struct{ goID, mdID string }{
		{"together", "togetherai"},
		{"github", "github-copilot"},
		{"kilo-gateway", "kilo"},
		{"codex", "openai"},
	} {
		if ProviderAliases[alias.goID] != alias.mdID {
			t.Errorf("ProviderAliases[%q] = %q, want %q", alias.goID, ProviderAliases[alias.goID], alias.mdID)
		}
	}
}
