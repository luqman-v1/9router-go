package providers

import "testing"

// TestGithubClaude55DottedCapabilities pins the upstream f3e1f716 contract for
// the dotted Claude 5.5 ids that GitHub Copilot (and Kiro / Token Harbor) list.
// Opus 5.5 cannot disable thinking and rejects forced tool_choice; Sonnet 5.5
// takes thinkingOffType "between_tools" in place of "disabled" and also rejects
// forced tool_choice. Both are claude-adaptive with a 1M/128000 envelope.
func TestGithubClaude55DottedCapabilities(t *testing.T) {
	opus := func() Capabilities {
		InvalidateCapabilitiesCache()
		return GetCapabilitiesForModel("github", "claude-opus-5.5")
	}
	sonnet := func() Capabilities {
		InvalidateCapabilitiesCache()
		return GetCapabilitiesForModel("github", "claude-sonnet-5.5")
	}
	haiku := func() Capabilities {
		InvalidateCapabilitiesCache()
		return GetCapabilitiesForModel("github", "claude-haiku-5.5")
	}

	t.Run("claude-opus-5.5", func(t *testing.T) {
		caps := opus()
		if caps.ThinkingFormat != "claude-adaptive" {
			t.Errorf("ThinkingFormat = %q, want claude-adaptive", caps.ThinkingFormat)
		}
		if caps.CanDisableThinking() {
			t.Error("ThinkingCanDisable must be false (Opus 5.5 cannot switch thinking off)")
		}
		if caps.CanForceToolChoice() {
			t.Error("ForcedToolChoice must be false (Opus 5.5 rejects forced tool_choice)")
		}
		if caps.ThinkingOffType != "" {
			t.Errorf("ThinkingOffType = %q, want empty (only Sonnet uses between_tools)", caps.ThinkingOffType)
		}
		if caps.ContextWindow != 1000000 || caps.MaxOutput != 128000 {
			t.Errorf("limits = %d/%d, want 1000000/128000", caps.ContextWindow, caps.MaxOutput)
		}
		if !caps.PDF {
			t.Error("PDF must be true")
		}
	})

	t.Run("claude-sonnet-5.5", func(t *testing.T) {
		caps := sonnet()
		if caps.ThinkingFormat != "claude-adaptive" {
			t.Errorf("ThinkingFormat = %q, want claude-adaptive", caps.ThinkingFormat)
		}
		if !caps.CanDisableThinking() {
			t.Error("ThinkingCanDisable must be true (Sonnet 5.5 can switch thinking off via between_tools)")
		}
		if caps.CanForceToolChoice() {
			t.Error("ForcedToolChoice must be false (Sonnet 5.5 rejects forced tool_choice)")
		}
		if caps.ThinkingOffType != "between_tools" {
			t.Errorf("ThinkingOffType = %q, want between_tools", caps.ThinkingOffType)
		}
	})

	t.Run("claude-haiku-5.5", func(t *testing.T) {
		caps := haiku()
		if caps.ThinkingFormat != "claude-adaptive" {
			t.Errorf("ThinkingFormat = %q, want claude-adaptive", caps.ThinkingFormat)
		}
		if !caps.CanDisableThinking() {
			t.Error("ThinkingCanDisable must be true (Haiku 5.5 can switch thinking off)")
		}
		if !caps.CanForceToolChoice() {
			t.Error("ForcedToolChoice must be true (Haiku 5.5 accepts forced tool_choice)")
		}
	})

	t.Run("opus-5.5 level list drops none", func(t *testing.T) {
		levels := GetThinkingLevels("github", "claude-opus-5.5")
		for _, l := range levels {
			if l == "none" {
				t.Errorf("GetThinkingLevels must not contain none for opus-5.5, got %v", levels)
			}
		}
	})
}
