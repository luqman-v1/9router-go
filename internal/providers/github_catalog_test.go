package providers

import (
	"slices"
	"testing"
)

// Upstream dbdbb648 refreshed the static Copilot fallback catalog: gpt-5-mini
// becomes the fallback default and the retired ids give way to the current
// documented models. Live account discovery stays authoritative.

func TestGitHubCatalogRefresh(t *testing.T) {
	catalog := ProviderModels["github"]
	if catalog[0] != "gpt-5-mini" {
		t.Errorf("github fallback default = %q, want gpt-5-mini", catalog[0])
	}
	for _, retired := range []string{
		"gpt-5.2", "gpt-5.2-codex", "claude-opus-4.5", "claude-opus-4.7",
		"claude-sonnet-4.5", "gemini-2.5-pro", "grok-code-fast-1",
		"oswe-vscode-prime", "goldeneye-free-auto",
	} {
		if slices.Contains(catalog, retired) {
			t.Errorf("github catalog still carries retired model %q", retired)
		}
	}
	for _, want := range []string{"gpt-6.1-sol", "claude-fable-5", "gemini-4-argon", "kimi-k3", "mai-code-1.1-flash"} {
		if !slices.Contains(catalog, want) {
			t.Errorf("github catalog lacks %q", want)
		}
	}
	if !slices.Equal(catalog, ProviderModels["gh"]) {
		t.Error("gh alias catalog diverged from github")
	}
}
