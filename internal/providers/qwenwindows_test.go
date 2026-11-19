package providers

import "testing"

// The qwen family rows carry upstream's declared windows
// (open-sse/providers/capabilities.js, master aba4c45d..1855aa44). Without
// them every qwen id fell through to lookupModelTokenLimits' 131072/8192
// floor: nous/qwen4-max published 131k while the provider's own /v1/models
// declares context_length 1000000, so clients compacted at an eighth of the
// real window. The vendor-prefixed nous spelling is the reported case.
func TestQwenFamilyWindows(t *testing.T) {
	tests := []struct {
		provider string
		model    string
		wantCW   int
		wantMax  int
	}{
		{"nous", "qwen/qwen4-max", 1000000, 65536},
		{"nous", "qwen/qwen3.8-max-prime", 1000000, 65536},
		{"nous", "qwen/qwen3.8-flash", 1000000, 65536},
		{"nous", "qwen/qwen3.8-omni-flash", 262144, 65536},
		{"openrouter", "qwen/qwen3.7-max", 1000000, 65536},
		{"openrouter", "qwen/qwen3.7-plus", 1000000, 65536},
		// Rows that declare only the window keep the substring table's output
		// figure (the Go port's documented precedence); the window itself is
		// upstream's declaration and what the reported bug was about.
		{"openrouter", "qwen/qwen3-coder", 1000000, 8192},
		{"openrouter", "qwen/qwen3-vl-plus", 262144, 8192},
		{"openrouter", "qwen/qwen3-235b-a22b", 262144, 8192},
		{"openrouter", "qwen/qwq-32b", 131072, 8192},
		{"openrouter", "qwen/qwen-turbo", 262144, 8192},
	}
	for _, tt := range tests {
		t.Run(tt.provider+"/"+tt.model, func(t *testing.T) {
			InvalidateCapabilitiesCache()
			detail := GetCapabilitiesDetailForModel(tt.provider, tt.model)
			if detail.ContextWindow != tt.wantCW || detail.MaxOutput != tt.wantMax {
				t.Errorf("limits = (%d, %d), want (%d, %d)",
					detail.ContextWindow, detail.MaxOutput, tt.wantCW, tt.wantMax)
			}
		})
	}
}

// The *qwen*max* row declares vision upstream; the Go port used to omit it,
// so an image sent to a qwen-max model was flattened away before the request.
func TestQwenMaxVision(t *testing.T) {
	caps := capabilitiesOf(t, "nous", "qwen/qwen4-max")
	if !caps.Vision {
		t.Error("Vision = false, want true (upstream *qwen*max* row)")
	}
	if caps.ThinkingFormat != "qwen" {
		t.Errorf("ThinkingFormat = %q, want qwen", caps.ThinkingFormat)
	}
}
