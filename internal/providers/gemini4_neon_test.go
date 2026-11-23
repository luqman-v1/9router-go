package providers

import (
	"9router/proxy/internal/pricing"
	"slices"
	"testing"
)

func TestGemini4NeonWiring(t *testing.T) {
	InvalidateCapabilitiesCache()
	t.Cleanup(InvalidateCapabilitiesCache)
	t.Run("caps: reasoning, 1M window, 65536 output", func(t *testing.T) {
		caps := GetCapabilitiesForModel("gemini", "gemini-4-neon")
		if !caps.Reasoning {
			t.Errorf("expected Reasoning=true")
		}
		if !caps.Vision {
			t.Errorf("expected Vision=true")
		}
		cw, maxOut := GetModelTokenLimits("gemini-4-neon")
		if cw != 1048576 {
			t.Errorf("expected contextWindow=1048576, got %d", cw)
		}
		if maxOut != 65536 {
			t.Errorf("expected maxOutput=65536, got %d", maxOut)
		}
	})

	t.Run("registered on antigravity and ag", func(t *testing.T) {
		for _, p := range []string{"antigravity", "ag", "agy-acp"} {
			models := ProviderModels[p]
			if !slices.Contains(models, "gemini-4-neon") {
				t.Errorf("provider %s does not contain gemini-4-neon", p)
			}
		}
	})
	t.Run("claude max tiers", func(t *testing.T) {
		for _, m := range []string{"claude-sonnet-5-5-max", "claude-opus-5-5-max"} {
			caps := GetCapabilitiesForModel("antigravity", m)
			if !caps.Reasoning {
				t.Errorf("%s expected Reasoning=true", m)
			}
		}
		ps, _ := pricing.GetPricingForModel("gemini", "claude-sonnet-5-5-max")
		_ = ps
		po, ok := pricing.GetPricingForModel("gemini", "claude-opus-5-5-max")
		if !ok {
			t.Fatalf("expected pricing for claude-opus-5-5-max")
		}
		if po.InputPer1M != 4 {
			t.Errorf("claude-opus-5-5-max InputPer1M = %v, want 4", po.InputPer1M)
		}
		pson, ok := pricing.GetPricingForModel("gemini", "claude-sonnet-5-5-max")
		if !ok {
			t.Fatalf("expected pricing for claude-sonnet-5-5-max")
		}
		if pson.InputPer1M != 2 {
			t.Errorf("claude-sonnet-5-5-max InputPer1M = %v, want 2", pson.InputPer1M)
		}
	})
}
