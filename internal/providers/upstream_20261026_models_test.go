package providers

import (
	"slices"
	"testing"

	"9router/proxy/internal/pricing"
)

// Upstream 2026-10-26 batch ports:
//   - 5cd4a75  feat(opencode-go): add Kimi K3.1 Pro
//   - a3bdaf6d feat(commandcode): add Kimi K3.1 Pro to the model catalog
//   - 1da5b0d7 fix(opencode): cap oc/deepseek-v4.1-flash-free context window at 330k
//
// These pin the catalog, capability and pricing data the upstream commits add.

func TestUpstream20261026RegistryContainsKimiK31Pro(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want string
	}{
		{"opencode-go alias", "opencode-go", "kimi-k3.1-pro"},
		{"ocg alias", "ocg", "kimi-k3.1-pro"},
		{"commandcode alias", "commandcode", "moonshotai/Kimi-K3.1-Pro"},
		{"cmc alias", "cmc", "moonshotai/Kimi-K3.1-Pro"},
		// Provider isolation: the generic opencode lane must not gain the
		// opencode-go id (upstream adds it only to opencode-go).
		{"opencode lane stays untouched", "opencode", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			models := GetProviderModels(tt.id)
			if tt.want != "" && !slices.Contains(models, tt.want) {
				t.Errorf("GetProviderModels(%q) missing %q", tt.id, tt.want)
			}
			if tt.want == "" && slices.Contains(models, "kimi-k3.1-pro") {
				t.Errorf("GetProviderModels(%q) unexpectedly contains kimi-k3.1-pro", tt.id)
			}
		})
	}
}

func TestUpstream20261026CommandCodeKimiK31Pro(t *testing.T) {
	models := GetProviderModels("commandcode")
	if !slices.Contains(models, "moonshotai/Kimi-K3.1-Pro") {
		t.Fatalf("GetProviderModels(commandcode) missing moonshotai/Kimi-K3.1-Pro")
	}
}

func TestUpstream20261026KimiK31ProCapabilities(t *testing.T) {
	caps := GetCapabilitiesForModel("opencode-go", "kimi-k3.1-pro")
	if !caps.Reasoning || !caps.Vision || !caps.VideoInput {
		t.Errorf("kimi-k3.1-pro capability flags = reasoning=%v vision=%v video=%v, want all true",
			caps.Reasoning, caps.Vision, caps.VideoInput)
	}
	if caps.ThinkingFormat != "kimi" {
		t.Errorf("kimi-k3.1-pro ThinkingFormat = %q, want kimi", caps.ThinkingFormat)
	}
	if caps.ThinkingCanDisable == nil || *caps.ThinkingCanDisable {
		t.Errorf("kimi-k3.1-pro ThinkingCanDisable must be false (Kimi thinking is always on)")
	}
	if caps.ContextWindow != 1048576 || caps.MaxOutput != 131072 {
		t.Errorf("kimi-k3.1-pro ContextWindow/MaxOutput = %d/%d, want 1048576/131072",
			caps.ContextWindow, caps.MaxOutput)
	}
	levels := GetThinkingLevels("opencode-go", "kimi-k3.1-pro")
	if !slices.Contains(levels, "max") {
		t.Errorf("GetThinkingLevels(opencode-go, kimi-k3.1-pro) = %v, want it to offer max", levels)
	}
}

func TestUpstream20261026OpencodeDeepseekFreeContextCap(t *testing.T) {
	caps := GetCapabilitiesForModel("opencode", "deepseek-v4.1-flash-free")
	if caps.ContextWindow != 330000 || caps.MaxOutput != 128000 {
		t.Errorf("deepseek-v4.1-flash-free ContextWindow/MaxOutput = %d/%d, want 330000/128000",
			caps.ContextWindow, caps.MaxOutput)
	}
	if !caps.Reasoning || !caps.Vision {
		t.Errorf("deepseek-v4.1-flash-free capability flags = reasoning=%v vision=%v, want both true",
			caps.Reasoning, caps.Vision)
	}
	if caps.ThinkingFormat != "deepseek" {
		t.Errorf("deepseek-v4.1-flash-free ThinkingFormat = %q, want deepseek", caps.ThinkingFormat)
	}
	// The context window must stay below max_tokens safety limits.
	if caps.MaxOutput > caps.ContextWindow {
		t.Errorf("MaxOutput %d exceeds ContextWindow %d", caps.MaxOutput, caps.ContextWindow)
	}
}

func TestUpstream20261026KimiK31ProPricing(t *testing.T) {
	p, ok := pricing.GetPricingForModel("opencode-go", "kimi-k3.1-pro")
	if !ok {
		t.Fatal("GetPricingForModel(opencode-go, kimi-k3.1-pro) not found")
	}
	want := pricing.ModelPricing{InputPer1M: 2, OutputPer1M: 10, CachedPer1M: 0.2, ReasoningPer1M: 10, CacheCreationPer1M: 2}
	if p != want {
		t.Errorf("kimi-k3.1-pro pricing = %+v, want %+v", p, want)
	}
}
