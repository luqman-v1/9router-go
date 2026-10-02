package providers

import (
	"slices"
	"testing"
)

// capabilitiesOf returns the resolved capability flags, after the cache reset
// each subtest needs.
func capabilitiesOf(t *testing.T, provider, model string) Capabilities {
	t.Helper()
	InvalidateCapabilitiesCache()
	return GetCapabilitiesForModel(provider, model)
}

// Port of upstream tests/unit/capabilities-sonnet-5-5.test.js (ccd0677d).
// Vendor-prefixed Sonnet 5.x ids have no exact entry and used to fall through
// to the generic *claude*sonnet* row, which serialises adaptive thinking as a
// token budget Anthropic no longer accepts.
func TestGetCapabilitiesForModel_ClaudeSonnet5Adaptive(t *testing.T) {
	models := []string{
		"claude-sonnet-5-5",
		"claude-sonnet-5.5",
		"anthropic/claude-sonnet-5-5",
		"anthropic/claude-sonnet-5",
		"openrouter/claude-sonnet-5",
	}
	for _, model := range models {
		t.Run(model, func(t *testing.T) {
			caps := capabilitiesOf(t, "claude", model)
			if caps.ThinkingFormat != "claude-adaptive" {
				t.Errorf("ThinkingFormat = %q, want claude-adaptive", caps.ThinkingFormat)
			}
			if !caps.Reasoning {
				t.Error("Reasoning = false, want true")
			}
		})
	}
}

// Port of upstream tests/unit/kiro-claude-opus-5-5.test.js (e78b766a) minus the
// registry-file assertion, which lives in registry_models.go: the four Opus 5.5
// spellings resolve to the 5.x adaptive family instead of the generic
// *claude*opus* budget row.
func TestGetCapabilitiesForModel_ClaudeOpus55(t *testing.T) {
	models := []string{
		"claude-opus-5.5",
		"claude-opus-5.5-thinking",
		"claude-opus-5.5-agentic",
		"claude-opus-5.5-thinking-agentic",
	}
	for _, model := range models {
		t.Run(model, func(t *testing.T) {
			caps := capabilitiesOf(t, "kiro", model)
			if caps.ThinkingFormat != "claude-adaptive" {
				t.Errorf("ThinkingFormat = %q, want claude-adaptive", caps.ThinkingFormat)
			}
			if !caps.Vision || !caps.Reasoning || !caps.Search {
				t.Errorf("want vision+reasoning+search, got %+v", caps)
			}
		})
	}
}

// The hyphenated deepseek-v4-1-flash id (upstream 8a4f4d9d) reached the
// text-only *deepseek-v4* row and lost both its vision flag and its 1M window.
func TestGetCapabilitiesForModel_DeepseekV41FlashAlias(t *testing.T) {
	for _, model := range []string{"deepseek-v4-1-flash", "deepseek-v4.1-flash", "kenari/deepseek-v4-1-flash"} {
		t.Run(model, func(t *testing.T) {
			caps := capabilitiesOf(t, "kenari", model)
			if !caps.Vision {
				t.Error("Vision = false, want true")
			}
			if caps.ThinkingFormat != "deepseek" {
				t.Errorf("ThinkingFormat = %q, want deepseek", caps.ThinkingFormat)
			}
		})
	}
}

// Port of upstream tests/unit/gpt-6-context-window.test.js (89ffac5a), minus
// the gateway rows 9router-go does not carry (Kiro 272k / Codex 272k+372k /
// Devin CLI 200k live in this port's provider tables, not in capabilities.js).
// The order of the tier rows is the load-bearing part: move mini/nano below
// *gpt-5.4* and they silently report 1.05M.
func TestGetGPTTokenWindows(t *testing.T) {
	const (
		apiWindow = 1050000
		tier400k  = 400000
	)
	tests := []struct {
		provider string
		model    string
		wantCW   int
	}{
		{provider: "github", model: "gpt-6-luna", wantCW: apiWindow},
		{provider: "azure", model: "gpt-6-luna", wantCW: apiWindow},
		{provider: "openai", model: "gpt-6-astra", wantCW: apiWindow},
		{provider: "openai", model: "gpt-5.4", wantCW: apiWindow},
		{provider: "openai", model: "gpt-5.4-pro", wantCW: apiWindow},
		{provider: "openai", model: "gpt-5.5", wantCW: apiWindow},
		{provider: "openai", model: "gpt-5.5-pro", wantCW: apiWindow},
		{provider: "openai", model: "gpt-5.6-terra", wantCW: apiWindow},
		{provider: "openai", model: "gpt-5.4-mini", wantCW: tier400k},
		{provider: "openai", model: "gpt-5.4-nano", wantCW: tier400k},
	}

	for _, tt := range tests {
		t.Run(tt.provider+"/"+tt.model, func(t *testing.T) {
			InvalidateCapabilitiesCache()
			detail := GetCapabilitiesDetailForModel(tt.provider, tt.model)
			if detail.ContextWindow != tt.wantCW || detail.MaxOutput != 128000 {
				t.Errorf("resolved limits = (%d, %d), want (%d, 128000)", detail.ContextWindow, detail.MaxOutput, tt.wantCW)
			}
			cw, out := GetModelTokenLimits(tt.model)
			if cw != tt.wantCW || out != 128000 {
				t.Errorf("GetModelTokenLimits = (%d, %d), want (%d, 128000)", cw, out, tt.wantCW)
			}
		})
	}
}

// Devin CLI is the only gateway in this port whose GPT rows carry their own
// truncation (upstream DEVIN_CLI_GPT_CAPS, 89ffac5a). Codex declares one too
// (272k for GPT-6, 872k for the [1m] variants), published per catalog entry by
// #93 — the generic 1.05M GPT-6 row must not win over it. That ordering is the
// regression worth pinning here: the caps row and the catalog row are resolved
// in sequence, and the catalog row is the narrower of the two on purpose.
// See TestGetGPTTokenWindows for the pattern rows themselves.
func TestGetGPTTokenWindows_GatewayOverrides(t *testing.T) {
	tests := []struct {
		provider string
		model    string
		wantCW   int
	}{
		{provider: "devin-cli", model: "gpt-5.4-high", wantCW: 200000},
		{provider: "devin-cli", model: "gpt-5.5-xhigh", wantCW: 200000},
		{provider: "codex", model: "gpt-6-astra", wantCW: 272000},
		{provider: "codex", model: "gpt-6-sol[1m]", wantCW: 872000},
	}

	for _, tt := range tests {
		t.Run(tt.provider+"/"+tt.model, func(t *testing.T) {
			InvalidateCapabilitiesCache()
			detail := GetCapabilitiesDetailForModel(tt.provider, tt.model)
			if detail.ContextWindow != tt.wantCW {
				t.Errorf("contextWindow = %d, want %d", detail.ContextWindow, tt.wantCW)
			}
		})
	}
}

// xhigh only became a real level for adaptive Claude in upstream 7894f3d3, and
// the two exclusion rows are what keeps 4.6 models off it. Both halves are
// pinned here: revert either the budgetX bump or a row and the set changes.
func TestGetThinkingLevels_ClaudeAdaptiveXHigh(t *testing.T) {
	tests := []struct {
		provider string
		model    string
		want     []string
	}{
		{provider: "claude", model: "claude-opus-5", want: []string{"none", "low", "medium", "high", "xhigh", "max"}},
		{provider: "claude", model: "claude-sonnet-5", want: []string{"none", "low", "medium", "high", "xhigh", "max"}},
		{provider: "kiro", model: "claude-sonnet-5", want: []string{"none", "low", "medium", "high", "xhigh", "max"}},
		{provider: "kiro", model: "claude-opus-5.5", want: []string{"none", "low", "medium", "high", "xhigh", "max"}},
		{provider: "claude", model: "claude-fable-5-1", want: []string{"low", "medium", "high", "xhigh", "max"}},
		{provider: "claude", model: "claude-opus-4.6", want: []string{"none", "low", "medium", "high", "max"}},
		{provider: "claude", model: "claude-opus-4-6", want: []string{"none", "low", "medium", "high", "max"}},
		{provider: "claude", model: "claude-sonnet-4.6", want: []string{"none", "low", "medium", "high", "max"}},
		{provider: "kiro", model: "claude-sonnet-4.6", want: []string{"none", "low", "medium", "high", "max"}},
	}

	for _, tt := range tests {
		t.Run(tt.provider+"/"+tt.model, func(t *testing.T) {
			InvalidateCapabilitiesCache()
			got := GetThinkingLevels(tt.provider, tt.model)
			if !slices.Equal(got, tt.want) {
				t.Errorf("GetThinkingLevels(%q, %q) = %v, want %v", tt.provider, tt.model, got, tt.want)
			}
		})
	}
}
