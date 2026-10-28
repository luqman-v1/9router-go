package providers

import (
	"slices"
	"testing"
)

// Grok 4.7 has a 500k window like 4.6, but had no pattern row of its own, so
// it fell through to the *grok-4* catch-all and the 128000/4096 limit floor.
// Combos report the smallest member window and history trimming cuts at 80%
// of it (upstream decolua/9router b0655314).

func TestGrok47ContextWindow(t *testing.T) {
	for _, provider := range []string{"xai", "grok-cli", "opencode-go", "tokenharbor"} {
		if got := GetCapabilitiesForModel(provider, "grok-4.7").ContextWindow; got != 500000 {
			t.Errorf("%s: grok-4.7 context window = %d, want 500000", provider, got)
		}
	}
	for _, id := range []string{"grok-4.7", "grok-4.7-build-fast"} {
		if got := GetCapabilitiesForModel("grok-cli", id).ContextWindow; got != 500000 {
			t.Errorf("grok-cli: %s context window = %d, want 500000", id, got)
		}
	}
}

func TestGrok47FeatureSet(t *testing.T) {
	caps := GetCapabilitiesForModel("grok-cli", "grok-4.7")
	if !caps.Vision || !caps.Reasoning || !caps.Search || !caps.Tools {
		t.Errorf("grok-4.7 feature set = %+v, want vision/reasoning/search/tools", caps)
	}
	if caps.ThinkingFormat != "openai" {
		t.Errorf("grok-4.7 thinking format = %q, want openai", caps.ThinkingFormat)
	}
	if caps.MaxOutput != 500000 {
		t.Errorf("grok-4.7 max output = %d, want 500000", caps.MaxOutput)
	}
}

func TestGrok47TokenLimits(t *testing.T) {
	cw, maxOut := GetModelTokenLimits("grok-4.7")
	if cw != 500000 || maxOut != 500000 {
		t.Errorf("grok-4.7 token limits = %d/%d, want 500000/500000", cw, maxOut)
	}
	if cw, _ := GetModelTokenLimits("grok-4"); cw == 500000 {
		t.Error("plain grok-4 must not pick up the 4.7 window")
	}
}

func TestGrok47InStaticLists(t *testing.T) {
	for _, provider := range []string{"xai", "grok-cli"} {
		if !slices.Contains(ProviderModels[provider], "grok-4.7") {
			t.Errorf("%s model list lacks grok-4.7", provider)
		}
	}
}
