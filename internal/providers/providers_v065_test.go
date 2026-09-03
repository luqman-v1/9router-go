package providers

import (
	"strings"
	"testing"
)

func TestClaudeFingerprint_21258(t *testing.T) {
	cfg, ok := KnownProviders["claude"]
	if !ok {
		t.Fatal("claude provider not found")
	}
	if got := cfg.StaticHeaders["User-Agent"]; got != "claude-cli/2.1.258 (external, sdk-cli)" {
		t.Errorf("expected User-Agent claude-cli/2.1.258, got %q", got)
	}
	if got := cfg.StaticHeaders["Anthropic-Beta"]; !strings.Contains(got, "prompt-caching-scope-2026-01-05") || !strings.Contains(got, "claude-code-20250219") {
		t.Errorf("Anthropic-Beta should contain full v0.5.65 beta list, got %q", got)
	}
	if cfg.StaticHeaders["Anthropic-Dangerous-Direct-Browser-Access"] != "true" {
		t.Error("expected Anthropic-Dangerous-Direct-Browser-Access true")
	}
	if cfg.StaticHeaders["X-App"] != "cli" {
		t.Error("expected X-App cli")
	}
}

func TestOllamaFetchConfig(t *testing.T) {
	cfg, ok := KnownProviders["ollama"]
	if !ok {
		t.Fatal("ollama provider not found")
	}
	if cfg.FetchURL != "https://ollama.com/api/web_fetch" {
		t.Errorf("expected FetchURL https://ollama.com/api/web_fetch, got %q", cfg.FetchURL)
	}
	if cfg.FetchMethod != "POST" {
		t.Errorf("expected FetchMethod POST, got %q", cfg.FetchMethod)
	}
	if cfg.BaseURL == "" {
		t.Error("ollama BaseURL should not be empty")
	}
}

func TestGemini38_Capabilities(t *testing.T) {
	caps := GetCapabilitiesForModel("", "gemini-3.8-flash-high")
	if !caps.Vision || !caps.Reasoning || !caps.Search || !caps.Tools {
		t.Errorf("gemini-3.8-flash-high should have Vision+Reasoning+Search+Tools, got %+v", caps)
	}
	caps2 := GetCapabilitiesForModel("antigravity", "gemini-3.8-flash")
	if !caps2.Vision || !caps2.AudioInput || !caps2.VideoInput {
		t.Errorf("gemini-3.8-flash should have Vision+AudioInput+VideoInput, got %+v", caps2)
	}
	caps3 := GetCapabilitiesForModel("", "gemini-3.8-flash-low")
	if !caps3.Reasoning {
		t.Errorf("gemini-3.8-flash-low should have Reasoning, got %+v", caps3)
	}
}

func TestMuseSpark_Capabilities(t *testing.T) {
	for _, model := range []string{"muse-spark-1.2-contributor-free", "muse-spark-1.3-contributor-free", "oc/muse-spark-1.3-contributor-free"} {
		caps := GetCapabilitiesForModel("opencode", model)
		if !caps.Vision || !caps.Reasoning || !caps.Tools {
			t.Errorf("%s should have Vision+Reasoning+Tools, got %+v", model, caps)
		}
	}
	// Pattern fallback
	caps := GetCapabilitiesForModel("", "muse-spark-2.0-free")
	if !caps.Vision || !caps.Reasoning {
		t.Errorf("pattern muse-spark should have Vision+Reasoning, got %+v", caps)
	}
}

func TestCodeBuddyCN_Catalog(t *testing.T) {
	// New models should be present
	for _, m := range []string{"hy3", "hy3-x", "hy4-preview", "hy4-preview-x", "glm-5.3", "glm-5.3-flash", "kimi-k3-1"} {
		caps := GetCapabilitiesForModel("codebuddy-cn", m)
		if !caps.Reasoning {
			t.Errorf("codebuddy-cn %s should have Reasoning, got %+v", m, caps)
		}
	}
	// EOL models removed: glm-5.0 and glm-4.7 should NOT be in codebuddy-cn map, fallback to pattern still has Reasoning but not as explicit
	// We check that the explicit map no longer contains them as primary (they would still match *glm* pattern, but we ensure they are not in the explicit map)
	// This is a soft check: they should at least be considered via pattern, not explicit
	for _, m := range []string{"glm-5.0", "glm-4.7"} {
		if _, ok := providerCapabilities["codebuddy-cn"][m]; ok {
			t.Errorf("codebuddy-cn %s should be removed (EOL), still present", m)
		}
	}
}

func TestModelTokenLimits_Gemini38(t *testing.T) {
	cw, maxOut := GetModelTokenLimits("gemini-3.8-flash-high")
	if cw != 1048576 || maxOut != 65536 {
		t.Errorf("expected 1048576/65536 for gemini-3.8, got %d/%d", cw, maxOut)
	}
}
