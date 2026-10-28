package providers

import (
	"slices"
	"testing"
)

// TestGetThinkingLevels_Parity pins the level sets to upstream's own vitest
// expectations (tests/unit/thinking-levels-gpt56-sol.test.js and
// thinking-levels-kiro.test.js), so a drift in the pattern table or the
// format table shows up as a failing test rather than a wrong picker.
func TestGetThinkingLevels_Parity(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		want     []string
	}{
		{name: "codex gpt-5.6-sol", provider: "codex", model: "gpt-5.6-sol",
			want: []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}},
		{name: "codex gpt-5.6-terra", provider: "codex", model: "gpt-5.6-terra",
			want: []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}},
		{name: "codex gpt-5.6-luna", provider: "codex", model: "gpt-5.6-luna",
			want: []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}},
		{name: "codex gpt-5.6-sol-review", provider: "codex", model: "gpt-5.6-sol-review",
			want: []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}},
		{name: "codex gpt-5.6-terra-review", provider: "codex", model: "gpt-5.6-terra-review",
			want: []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}},
		{name: "codex gpt-5.6-luna-review", provider: "codex", model: "gpt-5.6-luna-review",
			want: []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}},
		{name: "kiro gpt-5.6-sol has no codex-only overrides", provider: "kiro", model: "gpt-5.6-sol",
			want: []string{"none", "minimal", "low", "medium", "high", "xhigh"}},
		{name: "codex cannot disable thinking", provider: "codex", model: "gpt-5.3-codex",
			want: []string{"low", "medium", "high", "xhigh"}},
		{name: "commandcode", provider: "commandcode", model: "deepseek/deepseek-v4-pro",
			want: []string{"none", "low", "medium", "high", "xhigh", "max"}},
		{name: "commandcode via uiAlias", provider: "cmc", model: "moonshotai/Kimi-K2.7-Code",
			want: []string{"none", "low", "medium", "high", "xhigh", "max"}},
		{name: "zen free deepseek-v4.1-flash publishes low|high|max", provider: "opencode", model: "deepseek-v4.1-flash-free",
			want: []string{"low", "high", "max"}},
		{name: "zen free id on opencode-zen resolves the same", provider: "opencode-zen", model: "deepseek-v4.1-flash-free",
			want: []string{"low", "high", "max"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			InvalidateCapabilitiesCache()
			got := GetThinkingLevels(tt.provider, tt.model)

			if !slices.Equal(got, tt.want) {
				t.Errorf("GetThinkingLevels(%q, %q) = %v, want %v", tt.provider, tt.model, got, tt.want)
			}
		})
	}
}

// TestGetThinkingLevels_NoReasoning covers the two ways upstream returns null:
// no reasoning capability at all, and a Kiro model that cannot carry the effort
// field.
func TestGetThinkingLevels_NoReasoning(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
	}{
		{name: "kiro legacy claude", provider: "kiro", model: "claude-sonnet-4.5"},
		{name: "kiro non-claude", provider: "kiro", model: "glm-5"},
		{name: "text-only model", provider: "commandcode", model: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			InvalidateCapabilitiesCache()
			if got := GetThinkingLevels(tt.provider, tt.model); got != nil {
				t.Errorf("GetThinkingLevels(%q, %q) = %v, want nil", tt.provider, tt.model, got)
			}
		})
	}
}

// TestResolveKiroEffortPath checks the model gate behind the Kiro rows above:
// legacy Claude majors are rejected, newer ones use output_config, and GPT-5.6
// uses reasoning.
func TestResolveKiroEffortPath(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{model: "claude-sonnet-4.5", want: ""},
		{model: "claude-sonnet-4-5", want: ""},
		{model: "claude-3-5-sonnet", want: ""},
		{model: "claude-sonnet-5", want: "output_config"},
		{model: "claude-opus-4.6", want: "output_config"},
		{model: "gpt-5.6-sol", want: "reasoning"},
		{model: "gpt-5.6-luna", want: "reasoning"},
		{model: "glm-5", want: ""},
		{model: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			if got := ResolveKiroEffortPath(tt.model); got != tt.want {
				t.Errorf("ResolveKiroEffortPath(%q) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}

// TestCodexModelLevels covers the per-model `thinkingLevels` registry field
// (registry/codex.js) that upstream reads off the catalog entry. The GPT-6
// models are not in the Go catalog yet, so the fixture cannot exercise this
// path — the "codex"/cx provider split and the "(level)" suffix both need their
// own coverage here.
func TestCodexModelLevels(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		want     []string
	}{
		{name: "gpt-6-sol", provider: "codex", model: "gpt-6-sol",
			want: []string{"low", "medium", "high", "xhigh", "max"}},
		{name: "gpt-6-luna", provider: "codex", model: "gpt-6-luna",
			want: []string{"low", "medium", "high", "xhigh", "max"}},
		{name: "level suffix is stripped before the lookup", provider: "codex", model: "gpt-6-sol(high)",
			want: []string{"low", "medium", "high", "xhigh", "max"}},
		{name: "a model without a declared set", provider: "codex", model: "gpt-5.6-sol", want: nil},
		{name: "only codex consults the registry field", provider: "cx", model: "gpt-6-sol", want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := codexModelLevels(tt.provider, tt.model); !slices.Equal(got, tt.want) {
				t.Errorf("codexModelLevels(%q, %q) = %v, want %v", tt.provider, tt.model, got, tt.want)
			}
		})
	}
}

// TestMatchThinkingGlob pins the glob semantics: `*` must be able to cross a
// `/`, because CommandCode and CodeBuddy publish vendor-prefixed ids such as
// "deepseek/deepseek-v4-pro" and the tables are written against them.
func TestMatchThinkingGlob(t *testing.T) {
	tests := []struct {
		pattern string
		model   string
		want    bool
	}{
		// the dot in the pattern is literal, so this row targets the dot-versioned
		// v4.1 ids; "deepseek-v4-pro" is caught by the "deepseek-v4*" row instead
		{pattern: "*deepseek-v4.*", model: "deepseek/deepseek-v4.1-flash", want: true},
		{pattern: "*deepseek-v4.*", model: "deepseek-v4-flash", want: false},
		{pattern: "*deepseek-v4*", model: "deepseek/deepseek-v4-pro", want: true},
		{pattern: "*deepseek-v4.*", model: "deepseek-v3.2", want: false},
		{pattern: "glm-5.2", model: "glm-5.2", want: true},
		{pattern: "glm-5.2", model: "glm-5.2-flash", want: false},
		{pattern: "glm-5.3*", model: "glm-5.3-flash", want: true},
		{pattern: "*codex*", model: "gpt-5.3-codex", want: true},
		{pattern: "*codex*", model: "gpt-5.5", want: false},
		{pattern: "*gpt-5.6-sol*", model: "gpt-5.6-sol-review", want: true},
		{pattern: "*gpt-5.6-sol*", model: "gpt-5.6-luna", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"|"+tt.model, func(t *testing.T) {
			if got := matchThinkingGlob(tt.pattern, tt.model); got != tt.want {
				t.Errorf("matchThinkingGlob(%q, %q) = %v, want %v", tt.pattern, tt.model, got, tt.want)
			}
		})
	}
}
