package providers

import (
	"testing"
)

// TestGetCapabilitiesDetailForModel_CommandCode pins the CommandCode
// short-circuit: every model shares one wire, so limits and the
// effort-based thinking format come from the provider, and vision follows the
// text-only denylist. Upstream reference: capabilities.js:527-582.
func TestGetCapabilitiesDetailForModel_CommandCode(t *testing.T) {
	tests := []struct {
		name           string
		provider       string
		model          string
		wantVision     bool
		wantReasoning  bool
		wantThinkFmt   string
		wantEffortOK   bool
		wantCanDisable bool
		wantContext    int
		wantMaxOutput  int
	}{
		{
			name: "text-only deepseek pro",
			// denylisted, and the deepseek family pattern must not win
			provider: "commandcode", model: "deepseek/deepseek-v4-pro",
			wantVision: false, wantReasoning: true, wantThinkFmt: "commandcode",
			wantEffortOK: true, wantCanDisable: true,
			wantContext: 1000000, wantMaxOutput: 384000,
		},
		{
			name: "text-only glm matches case-insensitively",
			// the catalog spells it "GLM-5.2", the denylist "zai-org/glm-5.2"
			provider: "commandcode", model: "zai-org/GLM-5.2",
			wantVision: false, wantReasoning: true, wantThinkFmt: "commandcode",
			wantEffortOK: true, wantCanDisable: true,
			wantContext: 1000000, wantMaxOutput: 384000,
		},
		{
			name:     "nemotron is text-only",
			provider: "commandcode", model: "nvidia/nemotron-3-ultra-550b-a55b",
			wantVision: false, wantReasoning: true, wantThinkFmt: "commandcode",
			wantEffortOK: true, wantCanDisable: true,
			wantContext: 1000000, wantMaxOutput: 384000,
		},
		{
			name:     "kimi keeps vision",
			provider: "commandcode", model: "moonshotai/Kimi-K2.7-Code",
			wantVision: true, wantReasoning: true, wantThinkFmt: "commandcode",
			wantEffortOK: true, wantCanDisable: true,
			wantContext: 1000000, wantMaxOutput: 384000,
		},
		{
			name:     "minimax m3 is not on the denylist",
			provider: "commandcode", model: "MiniMaxAI/MiniMax-M3",
			wantVision: true, wantReasoning: true, wantThinkFmt: "commandcode",
			wantEffortOK: true, wantCanDisable: true,
			wantContext: 1000000, wantMaxOutput: 384000,
		},
		{
			name: "uiAlias cmc resolves the same block",
			// the dashboard links models as cmc/<vendor>/<model>
			provider: "cmc", model: "stepfun/Step-3.7-Flash",
			wantVision: true, wantReasoning: true, wantThinkFmt: "commandcode",
			wantEffortOK: true, wantCanDisable: true,
			wantContext: 1000000, wantMaxOutput: 384000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			InvalidateCapabilitiesCache()
			got := GetCapabilitiesDetailForModel(tt.provider, tt.model)

			if got.Vision != tt.wantVision {
				t.Errorf("Vision = %v, want %v", got.Vision, tt.wantVision)
			}
			if got.Reasoning != tt.wantReasoning {
				t.Errorf("Reasoning = %v, want %v", got.Reasoning, tt.wantReasoning)
			}
			if got.ThinkingFormat == nil {
				t.Fatalf("ThinkingFormat = nil, want %q", tt.wantThinkFmt)
			}
			if *got.ThinkingFormat != tt.wantThinkFmt {
				t.Errorf("ThinkingFormat = %q, want %q", *got.ThinkingFormat, tt.wantThinkFmt)
			}
			if got.ThinkingEffortSupported != tt.wantEffortOK {
				t.Errorf("ThinkingEffortSupported = %v, want %v", got.ThinkingEffortSupported, tt.wantEffortOK)
			}
			if got.ThinkingCanDisable != tt.wantCanDisable {
				t.Errorf("ThinkingCanDisable = %v, want %v", got.ThinkingCanDisable, tt.wantCanDisable)
			}
			if got.ContextWindow != tt.wantContext {
				t.Errorf("ContextWindow = %d, want %d", got.ContextWindow, tt.wantContext)
			}
			if got.MaxOutput != tt.wantMaxOutput {
				t.Errorf("MaxOutput = %d, want %d", got.MaxOutput, tt.wantMaxOutput)
			}
		})
	}
}

// TestIsCommandCodeTextOnly covers the denylist matcher: it lowercases the id
// and also matches the bare model name, so an extra vendor prefix still
// resolves (upstream capabilities.js:553).
func TestIsCommandCodeTextOnly(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{model: "deepseek/deepseek-v4-pro", want: true},
		{model: "DEEPSEEK/DeepSeek-V4-Pro", want: true},
		{model: "deepseek-v4-pro", want: true},               // bare name, no vendor prefix
		{model: "vendor/nested/deepseek-v4-pro", want: true}, // extra prefix segment
		{model: "nvidia/nemotron-3-ultra-550b-a55b", want: true},
		{model: "moonshotai/Kimi-K2.7-Code", want: false},
		{model: "MiniMaxAI/MiniMax-M3", want: false},
		{model: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			if got := isCommandCodeTextOnly(tt.model); got != tt.want {
				t.Errorf("isCommandCodeTextOnly(%q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}

// TestMergeCapabilities_ThinkingIsDeclarative guards the merge rule: an overlay
// that says nothing about thinking must keep the default, and an overlay that
// names a format but leaves ThinkingCanDisable unset must still resolve to
// "can disable" — that tri-state is the whole reason the field is a pointer.
func TestMergeCapabilities_ThinkingIsDeclarative(t *testing.T) {
	t.Run("overlay without a thinking format keeps the default", func(t *testing.T) {
		got := mergeCapabilities(DefaultCapabilities, Capabilities{Vision: true, Tools: true})

		if !got.CanDisableThinking() {
			t.Error("CanDisableThinking = false, want true")
		}
		if got.ThinkingFormat != "" {
			t.Errorf("ThinkingFormat = %q, want empty", got.ThinkingFormat)
		}
	})

	t.Run("overlay declaring a format without the flag keeps can-disable", func(t *testing.T) {
		got := mergeCapabilities(DefaultCapabilities, Capabilities{
			Tools:          true,
			ThinkingFormat: "zai",
		})

		if got.ThinkingFormat != "zai" {
			t.Errorf("ThinkingFormat = %q, want %q", got.ThinkingFormat, "zai")
		}
		if !got.CanDisableThinking() {
			t.Error("CanDisableThinking = false, want true (unset means upstream default)")
		}
	})

	t.Run("overlay declaring the whole thinking block wins", func(t *testing.T) {
		got := mergeCapabilities(DefaultCapabilities, Capabilities{
			Tools:              true,
			ThinkingFormat:     "zai",
			ThinkingCanDisable: new(false),
			ThinkingRange:      &ThinkingRange{Min: 1024, Max: 65536},
		})

		if got.ThinkingFormat != "zai" {
			t.Errorf("ThinkingFormat = %q, want %q", got.ThinkingFormat, "zai")
		}
		if got.CanDisableThinking() {
			t.Error("CanDisableThinking = true, want false")
		}
		if got.ThinkingRange == nil || got.ThinkingRange.Max != 65536 {
			t.Errorf("ThinkingRange = %+v, want {1024 65536}", got.ThinkingRange)
		}
	})
}
