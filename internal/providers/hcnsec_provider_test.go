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
	if len(models) != 19 {
		t.Errorf("expected 19 models for 'hcnsec', got %d", len(models))
	}
	hcModels := GetProviderModels("hc")
	if len(hcModels) != 19 {
		t.Errorf("expected 19 models for alias 'hc', got %d", len(hcModels))
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
	if kind := GetProviderModelKind("hcnsec", "Qwen3-Embedding-8B"); kind != "embedding" {
		t.Errorf("GetProviderModelKind('hcnsec', 'Qwen3-Embedding-8B') = %q, want 'embedding'", kind)
	}
	if kind := GetProviderModelKind("hcnsec", "stepaudio-2.5-asr"); kind != "stt" {
		t.Errorf("GetProviderModelKind('hcnsec', 'stepaudio-2.5-asr') = %q, want 'stt'", kind)
	}
	if kind := GetProviderModelKind("hcnsec", "stepaudio-2.5-tts"); kind != "tts" {
		t.Errorf("GetProviderModelKind('hcnsec', 'stepaudio-2.5-tts') = %q, want 'tts'", kind)
	}

	glmCaps := GetCapabilitiesForModel("hcnsec", "glm-5.3")
	if glmCaps.ContextWindow != 1000000 || glmCaps.MaxOutput != 131072 {
		t.Errorf("glm-5.3 caps: ContextWindow=%d, MaxOutput=%d", glmCaps.ContextWindow, glmCaps.MaxOutput)
	}
	qwenCaps := GetCapabilitiesForModel("hcnsec", "Qwen3.8-27B")
	if qwenCaps.ContextWindow != 200000 || qwenCaps.MaxOutput != 65536 {
		t.Errorf("Qwen3.8-27B caps: ContextWindow=%d, MaxOutput=%d", qwenCaps.ContextWindow, qwenCaps.MaxOutput)
	}
}
