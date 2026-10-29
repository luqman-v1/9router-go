package providers

import (
	"net/http"
	"testing"
)

func TestKnownProviders_HasExpectedEntries(t *testing.T) {
	wantProviders := []string{
		"openai", "anthropic", "deepseek", "groq", "nvidia", "openrouter",
		"cerebras", "together", "fireworks", "opencode", "gemini", "github",
		"mistral", "perplexity", "xai", "cohere", "ollama", "siliconflow",
		"cloudflare-ai", "mimo-free",
	}
	for _, p := range wantProviders {
		cfg, ok := KnownProviders[p]
		if !ok {
			t.Errorf("expected KnownProviders to contain %q", p)
			continue
		}
		if cfg.BaseURL == "" {
			t.Errorf("provider %q missing BaseURL", p)
		}
		if cfg.AuthHeader == "" {
			t.Errorf("provider %q missing AuthHeader", p)
		}
		if cfg.AuthScheme == "" {
			t.Errorf("provider %q missing AuthScheme", p)
		}
	}
}

func TestKnownProviders_AuthSchemes(t *testing.T) {
	// Anthropic uses raw auth (x-api-key header).
	if cfg := KnownProviders["anthropic"]; cfg.AuthScheme != "raw" || cfg.AuthHeader != "x-api-key" {
		t.Errorf("anthropic expected raw/x-api-key, got %s/%s", cfg.AuthScheme, cfg.AuthHeader)
	}
	// OpenAI uses bearer auth.
	if cfg := KnownProviders["openai"]; cfg.AuthScheme != "bearer" || cfg.AuthHeader != "Authorization" {
		t.Errorf("openai expected bearer/Authorization, got %s/%s", cfg.AuthScheme, cfg.AuthHeader)
	}
}

func TestKnownProviders_PerplexityWebIsParked(t *testing.T) {
	// Upstream 38a0c449 parks perplexity-web with serviceKinds: [] — no
	// executor sits behind it, so it must stay out of the pickers while the
	// cookie probe and any saved connection keep resolving. Hidden is that
	// contract on this side: offered nowhere, reachable everywhere.
	cfg := KnownProviders["perplexity-web"]
	if !cfg.Hidden {
		t.Error("perplexity-web expected Hidden=true (parked, upstream serviceKinds: [])")
	}
	if models := ProviderModels["perplexity-web"]; len(models) == 0 {
		t.Error("perplexity-web expected its model catalog kept so stored connections still resolve")
	}
	if got := ResolveAlias("pw"); got != "perplexity-web" {
		t.Errorf("ResolveAlias(pw) = %q, want perplexity-web", got)
	}
}

func TestKnownProviders_NoAuthOverridesScheme(t *testing.T) {
	// mimo-free has NoAuth=true and a DefaultAPIKey fallback.
	cfg := KnownProviders["mimo-free"]
	if !cfg.NoAuth {
		t.Error("mimo-free expected NoAuth=true")
	}
	if cfg.DefaultAPIKey == "" {
		t.Error("mimo-free expected a DefaultAPIKey fallback")
	}
	// G23b §6: the MMF entry is hidden — routable, not offered. Both its
	// spellings must point at the same model so a caller that uses the alias
	// gets the catalog the canonical id advertises.
	if !cfg.Hidden {
		t.Error("mimo-free expected Hidden=true (G23b)")
	}
	for _, id := range []string{"mimo-free", "mmf"} {
		if models := ProviderModels[id]; len(models) != 1 || models[0] != "mimo-auto" {
			t.Errorf("ProviderModels[%q] = %v, want [mimo-auto]", id, models)
		}
	}
	if got := ResolveAlias("mmf"); got != "mimo-free" {
		t.Errorf("ResolveAlias(mmf) = %q, want mimo-free", got)
	}
}

func TestKnownProviders_RequireClaudeToolType(t *testing.T) {
	for _, p := range []string{"minimax", "minimax-cn", "minimax-code", "minimax-code-global"} {
		cfg, ok := KnownProviders[p]
		if !ok {
			t.Fatalf("provider %s not found in KnownProviders", p)
		}
		if !cfg.RequireClaudeToolType {
			t.Errorf("provider %s expected RequireClaudeToolType=true", p)
		}
	}
	if KnownProviders["openai"].RequireClaudeToolType {
		t.Error("provider openai expected RequireClaudeToolType=false")
	}
}

func TestKnownProviders_StaticHeaders(t *testing.T) {
	cfg := KnownProviders["opencode"]
	if cfg.StaticHeaders["x-opencode-client"] != "cli" {
		t.Errorf("opencode expected static header x-opencode-client=cli, got %q", cfg.StaticHeaders["x-opencode-client"])
	}
}

func TestProviderAliasMap_Bidirectional(t *testing.T) {
	// Common aliases resolve to canonical providers.
	cases := map[string]string{
		"ds":   "deepseek",
		"oa":   "openai",
		"ant":  "anthropic",
		"gq":   "groq",
		"or":   "openrouter",
		"nv":   "nvidia",
		"cb":   "cerebras",
		"tg":   "together",
		"fw":   "fireworks",
		"gh":   "github",
		"pplx": "perplexity",
		"mmf":  "mimo-free",
	}
	for alias, canonical := range cases {
		got, ok := ProviderAliasMap[alias]
		if !ok {
			t.Errorf("expected alias %q to exist", alias)
			continue
		}
		if got != canonical {
			t.Errorf("alias %q: expected %q, got %q", alias, canonical, got)
		}
	}
}

func TestProviderAliasMap_NoSelfCycle(t *testing.T) {
	// An alias should never map to itself (would cause infinite resolution).
	for alias, canonical := range ProviderAliasMap {
		if alias == canonical {
			t.Errorf("alias %q maps to itself", alias)
		}
	}
}

func TestRetryableStatusCodes(t *testing.T) {
	if !RetryableStatusCodes[http.StatusUnauthorized] {
		t.Error("expected 401 to be retryable")
	}
	if !RetryableStatusCodes[http.StatusPaymentRequired] {
		t.Error("expected 402 to be retryable")
	}
	if !RetryableStatusCodes[http.StatusTooManyRequests] {
		t.Error("expected 429 to be retryable")
	}
	if RetryableStatusCodes[http.StatusBadRequest] {
		t.Error("400 should not be retryable")
	}
	if RetryableStatusCodes[http.StatusInternalServerError] {
		t.Error("500 should not be retryable")
	}
}

func TestNewProvidersRegistry_v055(t *testing.T) {
	if _, ok := KnownProviders["alitp-intl"]; !ok {
		t.Error("expected alitp-intl provider registered")
	}
	if _, ok := KnownProviders["fish-audio"]; !ok {
		t.Error("expected fish-audio provider registered")
	}
	if a := ResolveAlias("ali-tp"); a != "alitp-intl" {
		t.Errorf("expected ali-tp alias to resolve to alitp-intl, got %s", a)
	}
	if a := ResolveAlias("alitp"); a != "alitp-intl" {
		t.Errorf("expected alitp alias to resolve to alitp-intl, got %s", a)
	}
	if a := ResolveAlias("fish"); a != "fish-audio" {
		t.Errorf("expected fish alias to resolve to fish-audio, got %s", a)
	}
}

func TestNewProvidersRegistry_v059(t *testing.T) {
	if _, ok := KnownProviders["xquik"]; !ok {
		t.Error("expected xquik provider registered")
	}
	if _, ok := KnownProviders["ollama-search"]; !ok {
		t.Error("expected ollama-search provider registered")
	}
	if _, ok := KnownProviders["zai-search"]; !ok {
		t.Error("expected zai-search provider registered")
	}
	if a := ResolveAlias("xq"); a != "xquik" {
		t.Errorf("expected xq alias to resolve to xquik, got %s", a)
	}

	// Verify new model capabilities
	glmFlashCaps := GetCapabilitiesForModel("glm", "glm-5.3-flash")
	if !glmFlashCaps.Vision || !glmFlashCaps.Reasoning {
		t.Errorf("expected glm-5.3-flash to have vision and reasoning, got %+v", glmFlashCaps)
	}

	dsVisionCaps := GetCapabilitiesForModel("deepseek", "deepseek-v4-vision")
	if !dsVisionCaps.Vision || !dsVisionCaps.Reasoning {
		t.Errorf("expected deepseek-v4-vision to have vision and reasoning, got %+v", dsVisionCaps)
	}

	grokCaps := GetCapabilitiesForModel("xai", "grok-4.6")
	if !grokCaps.Reasoning || !grokCaps.Search {
		t.Errorf("expected grok-4.6 to have reasoning and search, got %+v", grokCaps)
	}
}

// Media models carry a `kind` upstream (image/tts/stt/embedding/video/
// systemone); /v1/models is the LLM list and must drop them.
func TestGetProviderModelKind(t *testing.T) {
	tests := []struct {
		provider string
		model    string
		want     string
	}{
		{provider: "nvidia", model: "fastpitch", want: "tts"},
		{provider: "nvidia", model: "tacotron2", want: "tts"},
		{provider: "nvidia", model: "nvidia/nv-embedqa-e5-v5", want: "embedding"},
		{provider: "nvidia", model: "nvidia/parakeet-ctc-1.1b-asr", want: "stt"},
		{provider: "nvidia", model: "minimaxai/minimax-m3", want: ""},
		{provider: "antigravity", model: "gemini-3.1-flash-image", want: "image"},
		{provider: "antigravity", model: "gemini-3.8-flash-high", want: ""},
		{provider: "openrouter", model: "openai/tts-1", want: "tts"},
		{provider: "openrouter", model: "google/veo-3.1", want: "video"},
		{provider: "unknown-provider", model: "whatever", want: ""},
		{provider: "nvidia", model: "not-a-model", want: ""},
	}
	for _, tt := range tests {
		if got := GetProviderModelKind(tt.provider, tt.model); got != tt.want {
			t.Errorf("GetProviderModelKind(%q, %q) = %q, want %q", tt.provider, tt.model, got, tt.want)
		}
	}
}

func TestNewProvidersRegistry_v060(t *testing.T) {
	// DaoXE
	if _, ok := KnownProviders["daoxe"]; !ok {
		t.Error("expected daoxe provider registered")
	}
	cfg := KnownProviders["daoxe"]
	if cfg.BaseURL != "https://api.daoxe.com/v1/chat/completions" {
		t.Errorf("daoxe expected BaseURL https://api.daoxe.com/v1/chat/completions, got %s", cfg.BaseURL)
	}
	if cfg.AuthScheme != "bearer" || cfg.AuthHeader != "Authorization" {
		t.Errorf("daoxe expected bearer/Authorization, got %s/%s", cfg.AuthScheme, cfg.AuthHeader)
	}

	// NaN.Builders
	if _, ok := KnownProviders["nan"]; !ok {
		t.Error("expected nan provider registered")
	}
	cfg = KnownProviders["nan"]
	if cfg.BaseURL != "https://api.nan.builders/v1/chat/completions" {
		t.Errorf("nan expected BaseURL https://api.nan.builders/v1/chat/completions, got %s", cfg.BaseURL)
	}
	if cfg.AuthScheme != "bearer" || cfg.AuthHeader != "Authorization" {
		t.Errorf("nan expected bearer/Authorization, got %s/%s", cfg.AuthScheme, cfg.AuthHeader)
	}

	// OpenAdapter
	if _, ok := KnownProviders["openadapter"]; !ok {
		t.Error("expected openadapter provider registered")
	}
	cfg = KnownProviders["openadapter"]
	if cfg.BaseURL != "https://api.openadapter.in/v1/chat/completions" {
		t.Errorf("openadapter expected BaseURL https://api.openadapter.in/v1/chat/completions, got %s", cfg.BaseURL)
	}
	if cfg.AuthScheme != "bearer" || cfg.AuthHeader != "Authorization" {
		t.Errorf("openadapter expected bearer/Authorization, got %s/%s", cfg.AuthScheme, cfg.AuthHeader)
	}

	// Aliases
	if a := ResolveAlias("dx"); a != "daoxe" {
		t.Errorf("expected dx alias to resolve to daoxe, got %s", a)
	}
	if a := ResolveAlias("nan"); a != "nan" {
		t.Errorf("expected nan alias to resolve to nan, got %s", a)
	}
	if a := ResolveAlias("oad"); a != "openadapter" {
		t.Errorf("expected oad alias to resolve to openadapter, got %s", a)
	}

	// Verify model capabilities
	gpt4oCaps := GetCapabilitiesForModel("daoxe", "gpt-4o")
	if !gpt4oCaps.Vision || !gpt4oCaps.Reasoning || !gpt4oCaps.Search || !gpt4oCaps.Tools {
		t.Errorf("expected daoxe/gpt-4o to have vision, reasoning, search, tools, got %+v", gpt4oCaps)
	}

	gpt4oCaps = GetCapabilitiesForModel("nan", "gpt-4o")
	if !gpt4oCaps.Vision || !gpt4oCaps.Reasoning || !gpt4oCaps.Search || !gpt4oCaps.Tools {
		t.Errorf("expected nan/gpt-4o to have vision, reasoning, search, tools, got %+v", gpt4oCaps)
	}

	gpt4oCaps = GetCapabilitiesForModel("openadapter", "gpt-4o")
	if !gpt4oCaps.Vision || !gpt4oCaps.Reasoning || !gpt4oCaps.Search || !gpt4oCaps.Tools {
		t.Errorf("expected openadapter/gpt-4o to have vision, reasoning, search, tools, got %+v", gpt4oCaps)
	}

	// Verify RegistryAliases
	if got := RegistryAliases["daoxe"]; got != "dx" {
		t.Errorf("RegistryAliases[daoxe] = %q, want dx", got)
	}
	if got := RegistryAliases["nan"]; got != "nan" {
		t.Errorf("RegistryAliases[nan] = %q, want nan", got)
	}
	if got := RegistryAliases["openadapter"]; got != "oad" {
		t.Errorf("RegistryAliases[openadapter] = %q, want oad", got)
	}

	// Verify models are registered for each provider
	expectedModel := map[string]string{
		"daoxe":       "claude-sonnet-4-6",
		"nan":         "deepseek-v4-flash",
		"openadapter": "glm-4.7",
	}
	for provider, wantModel := range expectedModel {
		models := GetProviderModels(provider)
		if len(models) == 0 {
			t.Errorf("GetProviderModels(%q) returned empty list", provider)
		}
		hasModel := false
		for _, m := range models {
			if m == wantModel {
				hasModel = true
				break
			}
		}
		if !hasModel {
			t.Errorf("GetProviderModels(%q) missing %s, got %v", provider, wantModel, models)
		}
	}
	// Verify aliases reach the same catalog
	for _, alias := range []string{"dx", "nan", "oad"} {
		models := GetProviderModels(alias)
		if len(models) == 0 {
			t.Errorf("GetProviderModels(%q) returned empty list", alias)
		}
	}
}

func TestNewProvidersRegistry_v061(t *testing.T) {
	// atmorouter
	if _, ok := KnownProviders["atmorouter"]; !ok {
		t.Error("expected atmorouter provider registered")
	}
	cfg := KnownProviders["atmorouter"]
	if cfg.BaseURL != "https://atmorouter.dev/v1/chat/completions" {
		t.Errorf("atmorouter expected BaseURL https://atmorouter.dev/v1/chat/completions, got %s", cfg.BaseURL)
	}
	if cfg.AuthScheme != "bearer" || cfg.AuthHeader != "Authorization" {
		t.Errorf("atmorouter expected bearer/Authorization, got %s/%s", cfg.AuthScheme, cfg.AuthHeader)
	}

	// runcrate
	if _, ok := KnownProviders["runcrate"]; !ok {
		t.Error("expected runcrate provider registered")
	}
	cfg = KnownProviders["runcrate"]
	if cfg.BaseURL != "https://api.runcrate.ai/v1/chat/completions" {
		t.Errorf("runcrate expected BaseURL https://api.runcrate.ai/v1/chat/completions, got %s", cfg.BaseURL)
	}
	if cfg.AuthScheme != "bearer" || cfg.AuthHeader != "Authorization" {
		t.Errorf("runcrate expected bearer/Authorization, got %s/%s", cfg.AuthScheme, cfg.AuthHeader)
	}

	// Aliases
	if a := ResolveAlias("atmo"); a != "atmorouter" {
		t.Errorf("expected atmo alias to resolve to atmorouter, got %s", a)
	}
	if a := ResolveAlias("rc"); a != "runcrate" {
		t.Errorf("expected rc alias to resolve to runcrate, got %s", a)
	}

	// Verify model seeds
	expectedModel := map[string]string{
		"atmorouter": "cc/claude-sonnet-4-6",
		"runcrate":   "deepseek-ai/DeepSeek-V4.1-Flash",
	}
	for provider, wantModel := range expectedModel {
		models := GetProviderModels(provider)
		if len(models) == 0 {
			t.Errorf("GetProviderModels(%q) returned empty list", provider)
		}
		hasModel := false
		for _, m := range models {
			if m == wantModel {
				hasModel = true
				break
			}
		}
		if !hasModel {
			t.Errorf("GetProviderModels(%q) missing %s, got %v", provider, wantModel, models)
		}
	}
	// Verify aliases reach the same catalog
	for _, alias := range []string{"atmo", "rc"} {
		models := GetProviderModels(alias)
		if len(models) == 0 {
			t.Errorf("GetProviderModels(%q) returned empty list", alias)
		}
	}
}
