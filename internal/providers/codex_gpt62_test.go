package providers

import (
	"testing"
)

func TestCodexGpt62SolAndAstraRegistered(t *testing.T) {
	InvalidateCapabilitiesCache()
	models := []string{"gpt-6.2-sol", "gpt-6.2-sol[1m]", "gpt-6.2-astra", "gpt-6.2-astra[1m]"}
	for _, provider := range []string{"codex", "cx"} {
		set := map[string]bool{}
		for _, m := range ProviderModels[provider] {
			set[m] = true
		}
		for _, m := range models {
			if !set[m] {
				t.Errorf("provider %q missing %q", provider, m)
			}
		}
	}
	for _, model := range models {
		caps := GetCapabilitiesDetailForModel("codex", model)
		if caps.ContextWindow <= 128000 {
			t.Errorf("model %q fell to floor: ContextWindow=%d", model, caps.ContextWindow)
		}
	}
	levels := GetThinkingLevels("codex", "gpt-6.2-sol")
	if len(levels) == 0 {
		t.Errorf("gpt-6.2-sol has no thinking levels")
	}
}
