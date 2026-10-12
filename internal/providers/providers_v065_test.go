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
	if got := cfg.StaticHeaders["User-Agent"]; got != "claude-cli/2.1.280 (external, sdk-cli)" {
		t.Errorf("expected User-Agent claude-cli/2.1.280, got %q", got)
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
func TestMuseSparkContributor_MandatoryReasoning(t *testing.T) {
	models := []struct{ provider, model string }{
		{"cline", "muse-spark-1.2-contributor"},
		{"cline", "muse-spark-1.3-contributor"},
		{"cline", "cline-free/muse-spark-1.2-contributor"},
		{"cline", "cline-free/muse-spark-1.3-contributor"},
		{"opencode", "muse-spark-1.2-contributor-free"},
		{"opencode", "muse-spark-1.3-contributor-free"},
	}
	for _, m := range models {
		caps := GetCapabilitiesForModel(m.provider, m.model)
		if !caps.Reasoning {
			t.Errorf("%s: Reasoning=false, want true", m.model)
		}
		if caps.ThinkingFormat != "openai" {
			t.Errorf("%s: ThinkingFormat=%q, want openai", m.model, caps.ThinkingFormat)
		}
		if caps.ThinkingCanDisable == nil || *caps.ThinkingCanDisable {
			t.Errorf("%s: thinking must not be disableable (got %+v)", m.model, caps.ThinkingCanDisable)
		}
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

// Gemini 4 (Argon) ids must resolve to their real window, not the 128k/4096
// floor. Both spellings occur: bridges publish the bare "gemini-argon" and
// gateways expose "gemini-4-argon".
func TestModelTokenLimits_Gemini4Argon(t *testing.T) {
	for _, id := range []string{"gemini-argon", "gemini-4-argon", "gsb/gemini-argon", "gemini-4-argon@think=high"} {
		cw, maxOut := GetModelTokenLimits(id)
		if cw != 1048576 || maxOut != 65536 {
			t.Errorf("%q: limits = (%d, %d), want (1048576, 65536)", id, cw, maxOut)
		}
	}
}

// The pattern row has to carry the Gemini 4 thinking shape: three levels, no
// minimal tier, thinking not disableable.
func TestCapabilities_Gemini4Argon(t *testing.T) {
	InvalidateCapabilitiesCache()
	t.Cleanup(InvalidateCapabilitiesCache)
	for _, id := range []string{"gemini-argon", "gemini-4-argon"} {
		caps := GetCapabilitiesForModel("gsb", id)
		if !caps.Vision || !caps.Reasoning || !caps.Search {
			t.Errorf("%q resolved as %+v, want vision+reasoning+search", id, caps)
		}
		if caps.ThinkingFormat != "gemini-level" {
			t.Errorf("%q thinkingFormat = %q, want gemini-level", id, caps.ThinkingFormat)
		}
		if caps.ThinkingCanDisable == nil || *caps.ThinkingCanDisable {
			t.Errorf("%q must not be able to disable thinking", id)
		}
	}
}

// Gemini 4 (Argon) takes LOW/MEDIUM/HIGH only: "minimal" is a 400 upstream and
// thinking cannot be disabled, so the picker must not offer either.
func TestThinkingLevels_Gemini4Argon(t *testing.T) {
	InvalidateCapabilitiesCache()
	t.Cleanup(InvalidateCapabilitiesCache)
	want := []string{"low", "medium", "high"}
	for _, id := range []string{"gemini-argon", "gemini-4-argon"} {
		got := GetThinkingLevels("gsb", id)
		if len(got) != len(want) {
			t.Fatalf("%q levels = %v, want %v", id, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%q levels = %v, want %v", id, got, want)
				break
			}
		}
	}
}
