package agy

import "strings"

// Model mapping follows AGENTS.md §3: explicit catalogue registration only,
// never substring-based provider switching or silent fallback. Keys are the
// model ids callers use with the agy-acp provider; values are the ids the
// local agy 1.5.9 binary advertises in session/new.
var agyModelIDs = map[string]bool{
	"gemini-4-carbon-high":     true,
	"gemini-4-carbon-medium":   true,
	"gemini-4-carbon-low":      true,
	"gemini-4-carbon":          true,
	"gemini-4-argon-high":      true,
	"gemini-4-argon-medium":    true,
	"gemini-4-argon-low":       true,
	"gemini-4-argon":           true,
	"gemini-4-hydrogen-xhigh":  true,
	"gemini-4-hydrogen-high":   true,
	"gemini-4-hydrogen-medium": true,
	"gemini-4-hydrogen-low":    true,
	"gemini-4-hydrogen":        true,
	"gemini-3.8-flash-high":    true,
	"gemini-3.8-flash-medium":  true,
	"gemini-3.8-flash-low":     true,
	"gemini-3.8-flash":         true,
	"gemini-3.7-flash-high":    true,
	"gemini-3.7-flash-medium":  true,
	"gemini-3.7-flash-low":     true,
	"gemini-3.1-pro-high":      true,
	"gemini-pro-agent":         true,
	"gemini-3.1-pro-low":       true,
	"claude-sonnet-5-5-high":   true,
	"claude-sonnet-5-5-medium": true,
	"claude-sonnet-5-5-low":    true,
	"claude-sonnet-5-5":        true,
	"claude-opus-5-5-high":     true,
	"claude-opus-5-5-medium":   true,
	"claude-opus-5-5-low":      true,
	"claude-opus-5-5":          true,
	"claude-sonnet-4-6":        true,
	"claude-opus-4-6-thinking": true,
	"gpt-oss-120b-medium":      true,
	"gpt-oss-120b":             true,
	"claude-sonnet":            true,
	"claude-opus":              true,
}

// ResolveModel maps a caller-supplied model id to an agy catalogue id.
// The bool reports whether the id was accepted; callers must return a clear
// error when false rather than falling back to a default model.
func ResolveModel(model string) (string, bool) {
	m := strings.TrimSpace(model)
	if m == "" {
		return "", false
	}
	if agyModelIDs[m] {
		return m, true
	}

	return "", false
}

// AvailableModels is the static agy 1.5.9 roster used for the provider
// catalogue and for validation when a live session/new roster is unavailable.
// AvailableModels is the full Antigravity roster exposed by the agy-acp
// provider, synced with internal/providers/registry_models.go. Models an
// account cannot select still list here and fail clearly at session/new.
func AvailableModels() []string {
	return []string{
		"gemini-4-carbon-high",
		"gemini-4-carbon-medium",
		"gemini-4-carbon-low",
		"gemini-4-carbon",
		"gemini-4-argon-high",
		"gemini-4-argon-medium",
		"gemini-4-argon-low",
		"gemini-4-argon",
		"gemini-4-hydrogen-xhigh",
		"gemini-4-hydrogen-high",
		"gemini-4-hydrogen-medium",
		"gemini-4-hydrogen-low",
		"gemini-4-hydrogen",
		"gemini-3.8-flash-high",
		"gemini-3.8-flash-medium",
		"gemini-3.8-flash-low",
		"gemini-3.8-flash",
		"gemini-3.7-flash-high",
		"gemini-3.7-flash-medium",
		"gemini-3.7-flash-low",
		"gemini-3.1-pro-high",
		"gemini-pro-agent",
		"gemini-3.1-pro-low",
		"claude-sonnet-5-5-high",
		"claude-sonnet-5-5-medium",
		"claude-sonnet-5-5-low",
		"claude-sonnet-5-5",
		"claude-opus-5-5-high",
		"claude-opus-5-5-medium",
		"claude-opus-5-5-low",
		"claude-opus-5-5",
		"claude-sonnet-4-6",
		"claude-opus-4-6-thinking",
		"gpt-oss-120b-medium",
		"gpt-oss-120b",
		"claude-sonnet",
		"claude-opus",
	}
}
