package providers

import (
	"strings"
	"testing"
)

func TestCommandcodeGoatLiveCatalogSync(t *testing.T) {
	// 97 models in the live Provider API feed snapshot
	if len(commandcodeGoatModels) < 90 {
		t.Fatalf("expected at least 90 models in commandcodeGoatModels, got %d", len(commandcodeGoatModels))
	}
	// Verify Claude models route to FormatClaude (only /messages)
	for _, id := range []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5", "claude-fable-5-2"} {
		target := CommandcodeGoatTarget(id)
		if target != FormatClaude {
			t.Errorf("model %q: target = %q, want %q", id, target, FormatClaude)
		}
		mf, ok := commandcodeGoatModelFormats[id]
		if !ok || !mf.SupportsFormat(FormatClaude) {
			t.Errorf("model %q: does not declare FormatClaude support", id)
		}
	}
	// Verify open models route to FormatOpenAI
	for _, id := range []string{"zai-org/GLM-5.3", "MiniMaxAI/MiniMax-M3", "Qwen/Qwen3.8-Max", "deepseek/deepseek-v4-pro"} {
		target := CommandcodeGoatTarget(id)
		if target != FormatOpenAI {
			t.Errorf("model %q: target = %q, want %q", id, target, FormatOpenAI)
		}
	}
	// Verify context window is model-specific, not hardcoded 1M
	if cw := commandcodeGoatContextWindows["MiniMaxAI/MiniMax-M2.5"]; cw != 200000 {
		t.Errorf("MiniMax-M2.5 context window = %d, want 200000", cw)
	}
	if cw := commandcodeGoatContextWindows["zai-org/GLM-5"]; cw != 200000 {
		t.Errorf("GLM-5 context window = %d, want 200000", cw)
	}
	if cw := commandcodeGoatContextWindows["moonshotai/Kimi-K2.5"]; cw != 256000 {
		t.Errorf("Kimi-K2.5 context window = %d, want 256000", cw)
	}
	if cw := commandcodeGoatContextWindows["zai-org/GLM-5.3"]; cw != 1000000 {
		t.Errorf("GLM-5.3 context window = %d, want 1000000", cw)
	}
}

func TestCommandcodeGoatProviderAliases(t *testing.T) {
	if got := ResolveAlias("cmc-goat"); got != "commandcode-goat" {
		t.Errorf("ResolveAlias(cmc-goat) = %q, want commandcode-goat", got)
	}
	if got := RegistryAliases["commandcode-goat"]; got != "cmc-goat" {
		t.Errorf("RegistryAliases[commandcode-goat] = %q, want cmc-goat", got)
	}
	if !isCommandCodeGoatProvider("commandcode-goat") {
		t.Errorf("isCommandCodeGoatProvider(commandcode-goat) = false")
	}
	if !isCommandCodeGoatProvider("cmc-goat") {
		t.Errorf("isCommandCodeGoatProvider(cmc-goat) = false")
	}
	// Must NOT match the legacy /alpha/generate provider
	if isCommandCodeProvider("commandcode-goat") {
		t.Errorf("isCommandCodeProvider(commandcode-goat) should be false")
	}
}

func TestCommandcodeGoatCapabilities(t *testing.T) {
	// Claude model gets claude-adaptive thinking format
	cClaude := commandCodeGoatCapabilities("claude-opus-5-5")
	if cClaude.ThinkingFormat != "claude-adaptive" {
		t.Errorf("claude-opus-5-5 ThinkingFormat = %q, want claude-adaptive", cClaude.ThinkingFormat)
	}
	if cClaude.ContextWindow != 1000000 {
		t.Errorf("claude-opus-5-5 ContextWindow = %d, want 1000000", cClaude.ContextWindow)
	}

	// Open model gets openai thinking format
	cOpen := commandCodeGoatCapabilities("zai-org/GLM-5.3")
	if cOpen.ThinkingFormat != "openai" {
		t.Errorf("GLM-5.3 ThinkingFormat = %q, want openai", cOpen.ThinkingFormat)
	}

	// 200k model gets 200k context window
	c200k := commandCodeGoatCapabilities("zai-org/GLM-5.1")
	if c200k.ContextWindow != 200000 {
		t.Errorf("GLM-5.1 ContextWindow = %d, want 200000", c200k.ContextWindow)
	}
}

func TestCommandcodeGoatModelsListURL(t *testing.T) {
	want := "https://api.commandcode.ai/provider/v1/models"
	if got := ModelsListURL("commandcode-goat"); got != want {
		t.Errorf("ModelsListURL(commandcode-goat) = %q, want %q", got, want)
	}
	if got := ModelsListURL("cmc-goat"); got != want {
		t.Errorf("ModelsListURL(cmc-goat) = %q, want %q", got, want)
	}
}

func TestCommandcodeGoatInKnownProviders(t *testing.T) {
	cfg, ok := KnownProviders["commandcode-goat"]
	if !ok {
		t.Fatal("KnownProviders missing commandcode-goat")
	}
	if !strings.HasPrefix(cfg.BaseURL, "https://api.commandcode.ai/provider/v1") {
		t.Errorf("unexpected BaseURL: %q", cfg.BaseURL)
	}
	if cfg.SystemoneURL != "https://api.commandcode.ai/provider/v1/systemone" {
		t.Errorf("unexpected SystemoneURL: %q", cfg.SystemoneURL)
	}
	if cfg.AuthScheme != "bearer" || cfg.AuthHeader != "Authorization" {
		t.Errorf("unexpected auth: %s / %s", cfg.AuthScheme, cfg.AuthHeader)
	}
}
