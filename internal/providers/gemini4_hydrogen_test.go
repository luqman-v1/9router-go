package providers

import (
	"slices"
	"testing"

	"9router/proxy/internal/pricing"
)

// Gemini 4 Hydrogen (2026-11): unified 1M window, tunable thinking low|medium|high|xhigh
// (no minimal), paid only, intro pricing $4/$20. Regression coverage for the
// registry/capabilities/pricing/thinking wiring.
func TestGemini4HydrogenWiring(t *testing.T) {
	InvalidateCapabilitiesCache()
	t.Cleanup(InvalidateCapabilitiesCache)

	t.Run("caps: reasoning, gemini-level, cannot disable, 1M window", func(t *testing.T) {
		caps := GetCapabilitiesForModel("gemini", "gemini-4-hydrogen")
		if !caps.Reasoning {
			t.Errorf("expected Reasoning=true")
		}
		if !caps.Vision {
			t.Errorf("expected Vision=true")
		}
		if caps.ThinkingFormat != "gemini-level" {
			t.Errorf("expected ThinkingFormat=gemini-level, got %q", caps.ThinkingFormat)
		}
		if caps.ThinkingCanDisable == nil || *caps.ThinkingCanDisable {
			t.Errorf("expected ThinkingCanDisable=false")
		}
		cw, maxOut := GetModelTokenLimits("gemini-4-hydrogen")
		if cw != 1048576 {
			t.Errorf("expected contextWindow=1048576, got %d", cw)
		}
		if maxOut != 65536 {
			t.Errorf("expected maxOutput=65536, got %d", maxOut)
		}
	})

	t.Run("thinking levels are low|medium|high|xhigh", func(t *testing.T) {
		got := GetThinkingLevels("gemini", "gemini-4-hydrogen")
		want := []string{"low", "medium", "high", "xhigh"}
		if !slices.Equal(got, want) {
			t.Errorf("GetThinkingLevels(gemini, gemini-4-hydrogen) = %v, want %v", got, want)
		}
	})

	t.Run("xhigh is advertised and passes through clamp", func(t *testing.T) {
		clamped := ClampGeminiLevel("gemini", "gemini-4-hydrogen", "xhigh")
		if clamped != "xhigh" {
			t.Errorf("ClampGeminiLevel(gemini-4-hydrogen, xhigh) = %q, want xhigh", clamped)
		}
		// minimal clamps to the floor (low), thinking cannot be disabled ("none" stripped).
		if clamped := ClampGeminiLevel("gemini", "gemini-4-hydrogen", "minimal"); clamped != "low" {
			t.Errorf("ClampGeminiLevel(gemini-4-hydrogen, minimal) = %q, want low", clamped)
		}
		if clamped := ClampGeminiLevel("gemini", "gemini-4-hydrogen", "none"); clamped != "high" {
			t.Errorf("ClampGeminiLevel(gemini-4-hydrogen, none) = %q, want high", clamped)
		}
	})

	t.Run("intro pricing $4/$20 with 95% cache discount", func(t *testing.T) {
		p, ok := pricing.GetPricingForModel("gemini", "gemini-4-hydrogen")
		if !ok {
			t.Fatalf("expected pricing for gemini-4-hydrogen")
		}
		want := pricing.ModelPricing{
			InputPer1M:         4,
			OutputPer1M:        20,
			CachedPer1M:        0.2,
			ReasoningPer1M:     20,
			CacheCreationPer1M: 0.375,
		}
		if p != want {
			t.Errorf("pricing = %+v, want %+v", p, want)
		}

		// Effort-annotated alias prices at base rate.
		pHigh, ok := pricing.GetPricingForModel("gemini", "gemini-4-hydrogen-high")
		if !ok {
			t.Fatalf("expected pricing for gemini-4-hydrogen-high")
		}
		if pHigh.InputPer1M != 4 {
			t.Errorf("pHigh.InputPer1M = %v, want 4", pHigh.InputPer1M)
		}
		pXHigh, ok := pricing.GetPricingForModel("gemini", "gemini-4-hydrogen-xhigh")
		if !ok {
			t.Fatalf("expected pricing for gemini-4-hydrogen-xhigh")
		}
		if pXHigh.InputPer1M != 4 {
			t.Errorf("pXHigh.InputPer1M = %v, want 4", pXHigh.InputPer1M)
		}
	})

	t.Run("registered on gemini, vertex, antigravity and ag", func(t *testing.T) {
		for _, p := range []string{"gemini", "vertex", "antigravity", "ag", "gc", "gemini-cli", "gh", "github"} {
			models := ProviderModels[p]
			if !slices.Contains(models, "gemini-4-hydrogen") {
				t.Errorf("provider %s does not contain gemini-4-hydrogen in ProviderModels", p)
			}
		}
	})
}
