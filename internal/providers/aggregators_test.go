package providers

import "testing"

// TestAggregatorProviders pins the five OpenAI-compatible aggregators upstream
// added in v0.5.91 (tokenharbor, dahl, atria, agnes, bai). They are registry
// entries like any other provider, so every table has to carry them: transport,
// alias resolution, executor registration and the offline model catalogue. A
// provider that reaches KnownProviders but not ProviderAliasMap is invisible
// from a `th/<model>` request, and one that reaches neither the dashboard nor
// the executor never routes at all.
func TestAggregatorProviders(t *testing.T) {
	tests := []struct {
		id          string
		baseURL     string
		models      []string
		aliases     []string
		modelsList  string
		wantFetcher bool
		authHeader  string
		authScheme  string
	}{
		{
			id: "tokenharbor", baseURL: "https://tokenharbor.ai/v1/chat/completions",
			models:     []string{"claude-opus-5.5", "claude-sonnet-5", "gpt-6-astra", "gpt-6-sol", "grok-4.7", "mimo-v2.6-flash:free", "mimo-v2.5:free", "qwen3.8-flash:free", "deepseek-v4.1-flash:free", "deepseek-v4-flash:free"},
			aliases:    []string{"th", "thh"},
			modelsList: "https://tokenharbor.ai/v1/models", wantFetcher: true,
		},
		{
			id: "dahl", baseURL: "https://inference.dahl.global/v1/chat/completions",
			models:     []string{"zai-org/GLM-5.3-Flash", "deepseek-ai/DeepSeek-V4-Flash-0731", "MiniMaxAI/MiniMax-M2.7"},
			aliases:    []string{"dahl-inference"},
			modelsList: "https://inference.dahl.global/v1/models", wantFetcher: true,
		},
		{
			id: "atria", baseURL: "https://api.atria-asi.ai/v1/chat/completions",
			models:     []string{"Atria-Dawn-Preview"},
			aliases:    []string{"atria-asi"},
			modelsList: "https://api.atria-asi.ai/v1/models",
		},
		{
			// agnes seeds a curated catalogue upstream (v0.5.95) because its live
			// /v1/models answers 401 without a token; bai still ships none.
			id: "agnes", baseURL: "https://apihub.agnes-ai.com/v1/chat/completions",
			aliases:    []string{"agnes-ai"},
			models:     []string{"agnes-2.5-flash", "agnes-2.5-pro", "agnes-2.5-pro-beta", "agnes-3.0-flash", "agnes-3.0-pro"},
			modelsList: "https://apihub.agnes-ai.com/v1/models",
		},
		{
			id: "bai", baseURL: "https://api.b.ai/v1/chat/completions",
			aliases:    []string{"b-ai"},
			modelsList: "https://api.b.ai/v1/models", wantFetcher: true,
		},
		{
			// Muse (Meta Model API): every model pins the Responses lane, so the
			// registry also carries a live catalogue endpoint for the dashboard.
			id: "muse", baseURL: "https://api.meta.ai/v1/chat/completions",
			models:     []string{"muse-spark-1.3", "muse-spark-1.2", "muse-spark-1.1", "muse-spark-1.3-contributor", "muse-spark-1.2-contributor"},
			aliases:    []string{"muse-ai", "meta-model-api", "muse-code", "muse-subscription"},
			modelsList: "https://api.meta.ai/v1/models", wantFetcher: true,
		},
		{
			// v1m answers System One only: no chat base URL, one endpoint.
			id: "v1m", baseURL: "",
			models:  []string{"rev-latest", "v1m-decision-engine"},
			aliases: []string{"systemone"},
		},
		{
			// SumoPod — Indonesian OpenAI-compatible gateway. Catalogue from
			// the live /v1/models response (61 chat models; embeddings excluded).
			id: "sumopod", baseURL: "https://ai.sumopod.com/v1/chat/completions",
			models:     []string{"MiniMax-M2.7-highspeed", "MiniMax-M3", "MiniMax-M3.1-Flash-Preview", "claude-fable-5-1", "claude-haiku-4-5", "claude-opus-4-8", "claude-opus-5", "claude-opus-5-5", "claude-sonnet-5", "deepseek-flash", "deepseek-v4-flash", "deepseek-v4-flash-0731:netra", "deepseek-v4-flash-vision-exp", "deepseek-v4-pro", "deepseek-v4.1-flash:netra", "gemini/gemini-3-flash-preview", "gemini/gemini-3.1-flash-lite", "gemini/gemini-3.1-pro-preview", "gemini/gemini-3.5-flash", "gemini/gemini-3.5-flash-lite", "gemini/gemini-3.7-flash", "gemini/gemini-3.8-flash", "glm-5", "glm-5-turbo", "glm-5.1", "glm-5.2", "glm-5.3-flash", "glm-5v-turbo", "gpt-4.1", "gpt-4.1-mini", "gpt-4.1-nano", "gpt-4o", "gpt-4o-mini", "gpt-5", "gpt-5-mini", "gpt-5-nano", "gpt-5.4", "gpt-5.4-mini", "gpt-5.4-nano", "gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-6-luna", "grok-4.7", "hy3", "kimi-k3", "mimo-v2.5", "mimo-v2.5-pro", "mimo-v2.6-flash", "mimo-v2.6-pro", "mimo-v2.6-pro-ultraspeed", "qwen3.6-flash", "qwen3.7-flash-2026-07-15", "qwen3.7-max", "qwen3.7-plus", "qwen3.8-flash", "qwen3.8-max", "seed-2-0-code", "seed-2-0-lite", "seed-2-0-mini", "seed-2-0-pro"},
			aliases:    []string{"sp"},
			modelsList: "https://ai.sumopod.com/v1/models", wantFetcher: true,
		},
		{
			id: "tinyfish", baseURL: "https://api.tinyfish.ai",
			authHeader: "x-api-key", authScheme: "raw",
		},
		{
			id: "nous", baseURL: "https://inference-api.nousresearch.com/v1/chat/completions",
			models: []string{
				"meituan/longcat-2.0:free",
				"meituan/longcat-2.5-preview:free",
				"stepfun/step-5-preview:free",
				"upstage/solar-mini4:free",
				"stepfun/step-3.7-flash:free",
				"poolside/laguna-s-2.1:free",
				"poolside/laguna-xs-2.1:free",
				"stealth/missingno",
			},
			aliases:    []string{"nous-portal", "nous-research", "ns"},
			modelsList: "https://inference-api.nousresearch.com/v1/models", wantFetcher: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			cfg, ok := KnownProviders[tt.id]
			if !ok {
				t.Fatalf("KnownProviders has no entry for %q", tt.id)
			}
			if cfg.BaseURL != tt.baseURL {
				t.Errorf("BaseURL = %q, want %q", cfg.BaseURL, tt.baseURL)
			}
			wantAuthHeader, wantAuthScheme := tt.authHeader, tt.authScheme
			if wantAuthHeader == "" {
				wantAuthHeader, wantAuthScheme = "Authorization", "bearer"
			}
			if cfg.AuthHeader != wantAuthHeader || cfg.AuthScheme != wantAuthScheme {
				t.Errorf("auth = %q/%q, want %q/%q", cfg.AuthHeader, cfg.AuthScheme, wantAuthHeader, wantAuthScheme)
			}

			if got := len(GetProviderModels(tt.id)); got != len(tt.models) {
				t.Errorf("catalog has %d models, want %d", got, len(tt.models))
			}
			for _, m := range tt.models {
				if !slicesContains(GetProviderModels(tt.id), m) {
					t.Errorf("catalog missing %q", m)
				}
			}

			for _, alias := range tt.aliases {
				if got := ResolveAlias(alias); got != tt.id {
					t.Errorf("ResolveAlias(%q) = %q, want %q", alias, got, tt.id)
				}
			}
			// uiAlias equals the id upstream, so an id is its own prefix.
			if got := ResolveAlias(tt.id); got != tt.id {
				t.Errorf("ResolveAlias(%q) = %q, want %q", tt.id, got, tt.id)
			}

			if got := ModelsListURL(tt.id); got != tt.modelsList {
				t.Errorf("ModelsListURL = %q, want %q", got, tt.modelsList)
			}
			if tt.wantFetcher && tt.modelsList == "" {
				t.Error("provider declares modelsFetcher upstream but has no models-list URL")
			}
		})
	}
}

func slicesContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
