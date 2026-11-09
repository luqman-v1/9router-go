package providers

import (
	"testing"
)

func TestGetCapabilitiesForModel_BedrockNormalization(t *testing.T) {
	tests := []struct {
		model             string
		wantThinkFmt      string
		wantCanDisable    *bool
		wantContextWindow int
		wantMaxOutput     int
	}{
		{
			model:        "us.anthropic.claude-opus-4-7",
			wantThinkFmt: "claude-adaptive",
		},
		{
			model:        "us.anthropic.claude-opus-4-8",
			wantThinkFmt: "claude-adaptive",
		},
		{
			model:             "us.anthropic.claude-fable-5-1",
			wantThinkFmt:      "claude-adaptive",
			wantCanDisable:    new(false),
			wantContextWindow: 1000000,
			wantMaxOutput:     128000,
		},
		{
			model:          "us.anthropic.claude-mythos-5-1",
			wantThinkFmt:   "claude-adaptive",
			wantCanDisable: new(false),
		},
		{
			model:        "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
			wantThinkFmt: "claude-budget",
		},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			InvalidateCapabilitiesCache()
			caps := GetCapabilitiesForModel("bedrock", tt.model)

			if caps.ThinkingFormat != tt.wantThinkFmt {
				t.Errorf("ThinkingFormat = %q, want %q", caps.ThinkingFormat, tt.wantThinkFmt)
			}
			if tt.wantCanDisable != nil {
				if caps.ThinkingCanDisable == nil || *caps.ThinkingCanDisable != *tt.wantCanDisable {
					t.Errorf("ThinkingCanDisable = %v, want %v", caps.ThinkingCanDisable, tt.wantCanDisable)
				}
			}
			if tt.wantContextWindow != 0 && caps.ContextWindow != tt.wantContextWindow {
				t.Errorf("ContextWindow = %d, want %d", caps.ContextWindow, tt.wantContextWindow)
			}
			if tt.wantMaxOutput != 0 && caps.MaxOutput != tt.wantMaxOutput {
				t.Errorf("MaxOutput = %d, want %d", caps.MaxOutput, tt.wantMaxOutput)
			}
		})
	}
}
