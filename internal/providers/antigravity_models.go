package providers

import "strings"

// Antigravity catalogue filter.
//
// Lives here rather than in the dashboard because the model picker needs it and
// the dashboard package must not own provider-catalogue rules.
//
// The quota panel deliberately keeps its own list: it must show
// gemini-3.1-flash-image, which this filter rejects as a non-chat surface, so
// sharing one list would silently drop a quota row an operator reads.
//
// The filter is a BLOCKLIST, not an allow-list, deliberately: the backend adds
// models faster than a curated list can track, so a new id is callable by
// default and only the known non-chat / retired surfaces are removed.

// AntigravityNonChatModelIDs are catalogue entries that are not user-callable
// chat models (image/TTS/tab-preview surfaces).
var AntigravityNonChatModelIDs = map[string]bool{
	"gemini-3-pro-image-preview":   true,
	"gemini-3.1-flash-image":       true,
	"gemini-3.1-flash-tts-preview": true,
	"gemini-2.5-flash-preview-tts": true,
	"tab_flash_lite_preview":       true,
	"tab_jump_flash_lite_preview":  true,
}

// AntigravityRetiredModelIDs are ids the upstream still advertises but which
// now return 400/404.
var AntigravityRetiredModelIDs = map[string]bool{
	"gemini-3-pro-preview":                    true,
	"gemini-3.1-pro":                          true,
	"gemini-3.6-flash-high":                   true,
	"gemini-3.6-flash-medium":                 true,
	"gemini-3.6-flash-low":                    true,
	"gemini-3-flash-agent":                    true,
	"gemini-3.5-flash":                        true,
	"gemini-3.5-flash-extra-low":              true,
	"gemini-3.5-flash-low":                    true,
	"gemini-3.5-flash-high":                   true,
	"gemini-3.5-flash-medium":                 true,
	"gemini-3.5-flash-preview":                true,
	"gemini-2.5-pro":                          true,
	"gemini-2.5-flash-thinking":               true,
	"gemini-2.5-flash":                        true,
	"gemini-2.5-flash-lite":                   true,
	"gemini-2.5-computer-use-preview-10-2025": true,
}

// IsDiscoverableAntigravityModel reports whether a catalogue id is a
// user-callable chat model.
//
// Rejects empty ids, the non-chat blocklist, the retired blocklist, the
// "chat_*" / "tab_*" internal slots, and ids whose segments name a non-chat
// surface (image/imagen/audio/tts/embedding/video/veo).
func IsDiscoverableAntigravityModel(modelID string) bool {
	id := strings.TrimSpace(modelID)
	if id == "" {
		return false
	}
	if AntigravityNonChatModelIDs[id] || AntigravityRetiredModelIDs[id] {
		return false
	}
	// Internal chat slots the backend reserves (e.g. chat_20706).
	if strings.HasPrefix(id, "chat_") || strings.HasPrefix(id, "tab_") {
		return false
	}
	// Non-chat surfaces by name. Matched on segment boundaries only, so a
	// legitimate id that merely contains the letters (e.g. "gemini-3-pro")
	// is never rejected.
	lower := strings.ToLower(id)
	for _, token := range []string{"image", "imagen", "audio", "tts", "embedding", "embed", "video", "veo"} {
		if lower == token ||
			strings.HasPrefix(lower, token+"-") ||
			strings.HasSuffix(lower, "-"+token) ||
			strings.Contains(lower, "-"+token+"-") {
			return false
		}
	}
	return true
}
