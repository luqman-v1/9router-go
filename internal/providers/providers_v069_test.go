package providers

import (
	"testing"
)

func TestGpt6Astra_Capabilities(t *testing.T) {
	caps := GetCapabilitiesForModel("codex", "gpt-6-astra")
	if !caps.Vision || !caps.Reasoning || !caps.Search || !caps.Tools {
		t.Errorf("gpt-6-astra should have Vision+Reasoning+Search+Tools, got %+v", caps)
	}

	// Pattern match test
	capsPattern := GetCapabilitiesForModel("", "custom-gpt-6-test")
	if !capsPattern.Vision || !capsPattern.Reasoning || !capsPattern.Search || !capsPattern.Tools {
		t.Errorf("custom-gpt-6-test should match *gpt-6* pattern, got %+v", capsPattern)
	}

	// The token window moved upstream (decolua/9router 89ffac5a): gpt-6 is a
	// 1.05M family, not one gateway's 272k truncation. GetGPTTokenWindows pins
	// the per-family numbers now.
}

func TestGpt56Image_Capabilities(t *testing.T) {
	for _, m := range []string{"gpt-5.6-sol-image", "gpt-5.6-terra-image", "gpt-5.6-luna-image"} {
		caps := GetCapabilitiesForModel("codex", m)
		if !caps.ImageOutput || !caps.Tools {
			t.Errorf("%s should have ImageOutput+Tools, got %+v", m, caps)
		}
	}
}

func TestCodeBuddyCN_V069_Capabilities(t *testing.T) {
	caps := GetCapabilitiesForModel("codebuddy-cn", "glm-5.2")
	if !caps.Vision || !caps.Reasoning || !caps.Tools {
		t.Errorf("codebuddy-cn glm-5.2 should have Vision+Reasoning+Tools, got %+v", caps)
	}
}
