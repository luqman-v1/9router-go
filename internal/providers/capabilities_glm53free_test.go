package providers

import (
	"slices"
	"testing"
)

// OpenCode serves glm-5.3-flash-free from its own inference service, which
// rejects the zai thinking object with 400 unknown field "thinking" while
// reasoning_effort is accepted (upstream decolua/9router 32c5f30a, probed
// live on /inference/v1 and /zen/v1).

func TestGLM53FlashFreeOpenAIThinking(t *testing.T) {
	for _, provider := range []string{"opencode", "opencode-zen"} {
		caps := GetCapabilitiesForModel(provider, "glm-5.3-flash-free")
		if caps.ThinkingFormat != "openai" {
			t.Errorf("%s: thinking format = %q, want openai", provider, caps.ThinkingFormat)
		}
		if canDisableThinking(caps) {
			t.Errorf("%s: glm-5.3-flash-free must not advertise a disable toggle", provider)
		}
		if !caps.Vision || !caps.PDF {
			t.Errorf("%s: capability = %+v, want vision and pdf", provider, caps)
		}
		levels := GetThinkingLevels(provider, "glm-5.3-flash-free")
		if !slices.Contains(levels, "high") {
			t.Errorf("%s: thinking levels %v lack high", provider, levels)
		}
		for _, banned := range []string{"thinking", "none"} {
			if slices.Contains(levels, banned) {
				t.Errorf("%s: thinking levels %v must not contain %q", provider, levels, banned)
			}
		}
	}
}

func TestGLM53FlashZaiUnchanged(t *testing.T) {
	if got := GetCapabilitiesForModel("glm", "glm-5.3-flash").ThinkingFormat; got != "zai" {
		t.Errorf("glm-5.3-flash thinking format = %q, want zai", got)
	}
}

func TestGLM52EffortSupported(t *testing.T) {
	// The exact glm-5.2 entry shadows the *glm-5.2* pattern; without the flag
	// repeated there, the zai lane drops reasoning_effort for GLM-5.2
	// (upstream decolua/9router b6a9ca43).
	if !GetCapabilitiesForModel("glm", "glm-5.2").ThinkingEffortSupported {
		t.Error("glm-5.2 must keep ThinkingEffortSupported on the exact entry")
	}
}
