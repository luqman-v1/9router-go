package providers

import (
	"strings"
	"testing"
)

func TestCodexRegistryDropsGpt55AndImageAndAutoPingAreGpt56(t *testing.T) {
	for _, provider := range []string{"codex", "cx"} {
		models, ok := ProviderModels[provider]
		if !ok {
			t.Fatalf("provider %q not found in ProviderModels", provider)
		}
		forbidden := []string{"gpt-5.5", "gpt-5.5-review", "gpt-5.5-image", "gpt-5.3-image"}
		for _, m := range models {
			for _, bad := range forbidden {
				if m == bad {
					t.Errorf("provider %q should not offer %q, but found in registry", provider, bad)
				}
			}
		}
	}

	if !strings.HasPrefix(CodexImagesMainModel, "gpt-5.6-") {
		t.Errorf("CodexImagesMainModel = %q, want a gpt-5.6-family model", CodexImagesMainModel)
	}

	if !strings.HasPrefix(CodexAutoPingModel, "gpt-5.6-") {
		t.Errorf("CodexAutoPingModel = %q, want a gpt-5.6-family model", CodexAutoPingModel)
	}

	// Live models for other providers must stay untouched
	otherProviders := []string{"bazaarlink", "bb", "blackbox", "bzl", "llm7", "openai", "ocz", "opencode-zen", "pa", "perplexity-agent", "tokenrouter"}
	for _, p := range otherProviders {
		models, ok := ProviderModels[p]
		if !ok {
			continue
		}
		found := false
		for _, m := range models {
			if m == "gpt-5.5" || m == "openai/gpt-5.5" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected provider %q to keep gpt-5.5", p)
		}
	}
}
