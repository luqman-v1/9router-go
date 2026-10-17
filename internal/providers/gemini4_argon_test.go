package providers

import (
	"slices"
	"testing"

	"9router/proxy/internal/pricing"
)

// Gemini 4 Argon (2026-10): unified 1M window, tunable thinking low|medium|high
// (no minimal), paid only, intro pricing $2/$10. Regression coverage for the
// registry/capabilities/pricing wiring (matching upstream tests/unit/gemini-4-argon.test.js).
func TestGemini4ArgonWiring(t *testing.T) {
	InvalidateCapabilitiesCache()
	t.Cleanup(InvalidateCapabilitiesCache)

	t.Run("caps: reasoning, gemini-level, cannot disable, 1M window", func(t *testing.T) {
		caps := GetCapabilitiesForModel("gemini", "gemini-4-argon")
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
		cw, maxOut := GetModelTokenLimits("gemini-4-argon")
		if cw != 1048576 {
			t.Errorf("expected contextWindow=1048576, got %d", cw)
		}
		if maxOut != 65536 {
			t.Errorf("expected maxOutput=65536, got %d", maxOut)
		}
	})

	t.Run("thinking levels are low|medium|high only", func(t *testing.T) {
		got := GetThinkingLevels("gemini", "gemini-4-argon")
		want := []string{"low", "medium", "high"}
		if !slices.Equal(got, want) {
			t.Errorf("GetThinkingLevels(gemini, gemini-4-argon) = %v, want %v", got, want)
		}
	})

	t.Run("unsupported levels clamp to the nearest advertised level", func(t *testing.T) {
		clamped := ClampGeminiLevel("gemini", "gemini-4-argon", "minimal")
		if clamped != "low" {
			t.Errorf("ClampGeminiLevel(gemini-4-argon, minimal) = %q, want low", clamped)
		}

		// Gemini 3.8 still accepts minimal
		clamped38 := ClampGeminiLevel("gemini", "gemini-3.8-flash", "minimal")
		if clamped38 != "minimal" {
			t.Errorf("ClampGeminiLevel(gemini-3.8-flash, minimal) = %q, want minimal", clamped38)
		}
	})

	t.Run("intro pricing $2/$10 with 95% cache discount", func(t *testing.T) {
		p, ok := pricing.GetPricingForModel("gemini", "gemini-4-argon")
		if !ok {
			t.Fatalf("expected pricing for gemini-4-argon")
		}
		want := pricing.ModelPricing{
			InputPer1M:         2,
			OutputPer1M:        10,
			CachedPer1M:        0.1,
			ReasoningPer1M:     10,
			CacheCreationPer1M: 2,
		}
		if p != want {
			t.Errorf("pricing = %+v, want %+v", p, want)
		}

		// Effort-annotated alias prices at base rate
		pHigh, ok := pricing.GetPricingForModel("gemini", "gemini-4-argon-high")
		if !ok {
			t.Fatalf("expected pricing for gemini-4-argon-high")
		}
		if pHigh.InputPer1M != 2 {
			t.Errorf("pHigh.InputPer1M = %v, want 2", pHigh.InputPer1M)
		}
	})

	t.Run("registered on gemini, vertex, gemini-cli and antigravity", func(t *testing.T) {
		for _, p := range []string{"gemini", "vertex", "gemini-cli", "antigravity", "ag"} {
			models := ProviderModels[p]
			if !slices.Contains(models, "gemini-4-argon") {
				t.Errorf("provider %s does not contain gemini-4-argon in ProviderModels", p)
			}
		}
	})
}
