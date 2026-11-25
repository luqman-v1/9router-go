package providers

import "testing"

func TestCloudflareAIModelsPruneDeprecatedAndAddReplacements(t *testing.T) {
	retired := []string{
		"@cf/meta/llama-3.2-1b-instruct",
		"@cf/meta/llama-3.2-3b-instruct",
		"@cf/meta/llama-3.1-8b-instruct-fp8-fast",
		"@cf/meta/llama-3.1-70b-instruct-fp8-fast",
		"@cf/deepseek-ai/deepseek-r1-distill-qwen-32b",
		"@cf/moonshotai/kimi-k2.6",
		"@cf/qwen/qwq-32b",
		"@cf/bytedance/stable-diffusion-xl-lightning",
		"@cf/lykon/dreamshaper-8-lcm",
		"@cf/runwayml/stable-diffusion-v1-5-img2img",
		"@cf/runwayml/stable-diffusion-v1-5-inpainting",
		"@cf/stabilityai/stable-diffusion-xl-base-1.0",
	}
	replacements := []string{
		"@cf/meta/llama-4-scout-17b-16e-instruct",
		"@cf/deepseek-ai/deepseek-v4.1-flash-1017",
		"@cf/google/gemma-4-26b-a4b-it",
		"@cf/zai-org/glm-5.3",
		"@cf/openai/gpt-oss-120b",
		"@cf/openai/gpt-oss-20b",
		"@cf/qwen/qwen3.8-27b",
	}
	for _, key := range []string{"cf", "cloudflare-ai"} {
		models := ProviderModels[key]
		for _, bad := range retired {
			for _, m := range models {
				if m == bad {
					t.Errorf("provider %q should not offer deprecated model %q", key, bad)
				}
			}
		}
		for _, good := range replacements {
			found := false
			for _, m := range models {
				if m == good {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("provider %q missing replacement model %q", key, good)
			}
		}
	}
}
