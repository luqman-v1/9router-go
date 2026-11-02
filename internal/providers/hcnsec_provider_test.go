package providers

import (
	"testing"
)

func TestHCNSecProvider_Config(t *testing.T) {
	cfg, ok := KnownProviders["hcnsec"]
	if !ok {
		t.Fatal("expected KnownProviders to contain 'hcnsec'")
	}
	if cfg.BaseURL != "https://api.hcnsec.cn/v1/chat/completions" {
		t.Errorf("expected BaseURL https://api.hcnsec.cn/v1/chat/completions, got %q", cfg.BaseURL)
	}
	if cfg.AuthHeader != "Authorization" || cfg.AuthScheme != "bearer" {
		t.Errorf("expected Authorization/bearer, got %s/%s", cfg.AuthHeader, cfg.AuthScheme)
	}
	if cfg.ThinkingFormat != "openai" {
		t.Errorf("expected ThinkingFormat 'openai', got %q", cfg.ThinkingFormat)
	}
}

func TestHCNSecProvider_Aliases(t *testing.T) {
	if got := ResolveAlias("hc"); got != "hcnsec" {
		t.Errorf("ResolveAlias('hc') = %q, want 'hcnsec'", got)
	}
	if got := RegistryAliases["hcnsec"]; got != "hc" {
		t.Errorf("RegistryAliases['hcnsec'] = %q, want 'hc'", got)
	}
}

func TestHCNSecProvider_Models(t *testing.T) {
	models := GetProviderModels("hcnsec")
	if len(models) == 0 {
		t.Error("expected non-empty model list for 'hcnsec'")
	}
	hcModels := GetProviderModels("hc")
	if len(hcModels) == 0 {
		t.Error("expected non-empty model list for alias 'hc'")
	}
}

func TestHCNSecProvider_Capabilities(t *testing.T) {
	caps := GetCapabilitiesForModel("hcnsec", "DeepSeek-V4-Flash")
	if !caps.Vision {
		t.Error("expected Vision=true for DeepSeek-V4-Flash on hcnsec")
	}
	if !caps.Tools {
		t.Error("expected Tools=true for DeepSeek-V4-Flash on hcnsec")
	}
}
