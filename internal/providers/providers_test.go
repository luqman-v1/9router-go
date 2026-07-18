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

func TestKnownProviders_NoAuthOverridesScheme(t *testing.T) {
	// mimo-free has NoAuth=true and a DefaultAPIKey fallback.
	cfg := KnownProviders["mimo-free"]
	if !cfg.NoAuth {
		t.Error("mimo-free expected NoAuth=true")
	}
	if cfg.DefaultAPIKey == "" {
		t.Error("mimo-free expected a DefaultAPIKey fallback")
	}
}

func TestKnownProviders_StaticHeaders(t *testing.T) {
	cfg := KnownProviders["opencode"]
	if cfg.StaticHeaders["x-opencode-client"] != "desktop" {
		t.Errorf("opencode expected static header x-opencode-client=desktop, got %q", cfg.StaticHeaders["x-opencode-client"])
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
