package agy

import "strings"

// Model mapping follows AGENTS.md §3: explicit catalogue registration only,
// never substring-based provider switching or silent fallback. Keys are the
// model ids callers use with the agy-acp provider; values are the ids the
// local agy 1.5.9 binary advertises in session/new.
var agyModelIDs = map[string]bool{
	"gemini-3.8-flash-high":    true,
	"gemini-3.8-flash-medium":  true,
	"gemini-3.8-flash-low":     true,
	"claude-sonnet-4-6":        true,
	"claude-opus-4-6-thinking": true,
	"gpt-oss-120b-medium":      true,
}

// aliases maps short 9router-friendly ids onto the exact agy catalogue id.
// Only entries that genuinely exist upstream are listed.
var aliases = map[string]string{
	"gemini-3.8-flash": "gemini-3.8-flash-medium",
	"claude-sonnet":    "claude-sonnet-4-6",
	"claude-opus":      "claude-opus-4-6-thinking",
	"gpt-oss-120b":     "gpt-oss-120b-medium",
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
	if canonical, ok := aliases[strings.ToLower(m)]; ok {
		return canonical, true
	}
	return "", false
}

// AvailableModels is the static agy 1.5.9 roster used for the provider
// catalogue and for validation when a live session/new roster is unavailable.
func AvailableModels() []string {
	return []string{
		"gemini-3.8-flash-high",
		"gemini-3.8-flash-medium",
		"gemini-3.8-flash-low",
		"claude-sonnet-4-6",
		"claude-opus-4-6-thinking",
		"gpt-oss-120b-medium",
	}
}
