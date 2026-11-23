package providers

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Port of upstream decolua/9router#ecb4d49eb (tests/unit/cloudflare-ai-registry.test.js):
// Cloudflare retired 24 Workers AI models on 2026-11-20 and never shipped two
// more that were still seeded here. Both Go lookup lanes (provider id and
// short alias) must mirror the pruned + extended upstream registry.
func TestCloudflareRegistrySeedsPrunedAndExtended(t *testing.T) {
	providerIDs := []string{"cf", "cloudflare-ai"}

	retired := []string{
		"@cf/meta/llama-3.2-1b-instruct",
		"@cf/meta/llama-3.2-3b-instruct",
		"@cf/meta/llama-3.1-8b-instruct-fp8",
		"@cf/meta/llama-3.1-8b-instruct-fp8-fast",
		"@cf/meta/llama-3.1-70b-instruct-fp8-fast",
		"@cf/qwen/qwq-32b",
		"@cf/deepseek-ai/deepseek-r1-distill-qwen-32b",
		"@cf/moonshotai/kimi-k2.6",
		"@cf/bytedance/stable-diffusion-xl-lightning",
		"@cf/lykon/dreamshaper-8-lcm",
		"@cf/runwayml/stable-diffusion-v1-5-img2img",
		"@cf/runwayml/stable-diffusion-v1-5-inpainting",
		"@cf/stabilityai/stable-diffusion-xl-base-1.0",
	}

	// id -> upstream display name; keep the upstream spelling verbatim.
	replacements := map[string]string{
		"@cf/meta/llama-4-scout-17b-16e-instruct":  "Llama 4 Scout 17B Instruct",
		"@cf/deepseek-ai/deepseek-v4.1-flash-1017": "DeepSeek V4.1 Flash",
		"@cf/google/gemma-4-26b-a4b-it":            "Gemma 4 26B A4B IT",
		"@cf/zai-org/glm-5.3":                      "GLM 5.3",
		"@cf/openai/gpt-oss-120b":                  "GPT OSS 120B",
		"@cf/openai/gpt-oss-20b":                   "GPT OSS 20B",
		"@cf/qwen/qwen3.8-27b":                     "Qwen 3.8 27B",
	}

	// The display-name map in this repo is the dashboard's hand-maintained
	// copy of the upstream registry. The "cf" alias resolves to the same
	// cloudflare-ai block. Missing block means no name map to assert against.
	raw, err := os.ReadFile("../../web/src/lib/models.ts")
	if err != nil {
		t.Fatalf("read dashboard catalog: %v", err)
	}
	block := dashboardCatalogBlock(string(raw), "cloudflare-ai")
	nameByID := map[string]string{}
	if block != "" {
		for _, m := range regexp.MustCompile(`"id":\s*"([^"]+)",\s*"name":\s*"([^"]+)"`).FindAllStringSubmatch(block, -1) {
			nameByID[m[1]] = m[2]
		}
	}

	for _, provider := range providerIDs {
		t.Run(provider, func(t *testing.T) {
			models := ProviderModels[provider]
			if len(models) == 0 {
				t.Fatalf("ProviderModels[%q] is empty", provider)
			}
			seeded := make(map[string]bool, len(models))
			for _, id := range models {
				if seeded[id] {
					t.Errorf("duplicate seed id %q", id)
				}
				seeded[id] = true
				if !strings.HasPrefix(id, "@cf/") {
					t.Errorf("seed id %q does not start with @cf/", id)
				}
				if block != "" {
					if name := nameByID[id]; name == "" {
						t.Errorf("seed id %q has no display name in the cloudflare-ai dashboard catalog", id)
					}
				}
			}

			for _, id := range retired {
				if seeded[id] {
					t.Errorf("retired/absent id %q is still seeded", id)
				}
				if block != "" {
					if _, ok := nameByID[id]; ok {
						t.Errorf("retired/absent id %q is still listed in the dashboard catalog", id)
					}
				}
				if kind, ok := ProviderModelKinds[provider][id]; ok {
					t.Errorf("retired/absent id %q still has service kind %q", id, kind)
				}
			}

			for id, wantName := range replacements {
				if !seeded[id] {
					t.Errorf("active replacement %q is missing from the seed list", id)
				}
				if kind, ok := ProviderModelKinds[provider][id]; ok {
					t.Errorf("LLM replacement %q must not have service kind %q", id, kind)
				}
				if block != "" {
					if got := nameByID[id]; got != wantName {
						t.Errorf("display name for %q = %q, want %q", id, got, wantName)
					}
				}
			}

			// GetProviderModels resolves aliases, so it must agree with the map.
			for _, id := range models {
				found := false
				for _, resolved := range GetProviderModels(provider) {
					if resolved == id {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("GetProviderModels(%q) is missing map seed %q", provider, id)
				}
			}
		})
	}

	// Both lanes must stay identical: short alias and provider id route the
	// same catalogue, as upstream parity requires.
	cfModels := ProviderModels["cf"]
	cfaModels := ProviderModels["cloudflare-ai"]
	if len(cfModels) != len(cfaModels) {
		t.Fatalf("cf has %d seeds, cloudflare-ai has %d", len(cfModels), len(cfaModels))
	}
	for i := range cfModels {
		if cfModels[i] != cfaModels[i] {
			t.Fatalf("seed %d differs: cf=%q cloudflare-ai=%q", i, cfModels[i], cfaModels[i])
		}
	}
}
