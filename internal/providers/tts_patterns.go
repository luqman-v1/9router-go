package providers

import (
	"regexp"
	"strings"
)

// Name-based speech-synthesis detection — last resort when neither the
// registry's per-provider service kinds nor the capability tables know a model.
//
// It exists because the registry classification cannot reach a custom provider
// node: GetProviderModelKind needs an exact ProviderModelKinds[provider][model]
// hit, and an operator-defined node ("openai-compatible-chat-studio", prefix
// "studio") has no registry entry and no alias, so studio/gemini-3.8-flash-tts
// resolved with audioOutput false while its endpoint answered audio/wav. The
// same gap hits a builtin provider that carries a TTS id without declaring its
// kind (vertex, ag) and the eight mimo TTS pairs on xiaomi-tokenplan / xmtp.
//
// Only ever turns audio output ON. Never used to turn a declared capability
// off — upstream advertises thinking levels for its TTS models, so clearing
// Reasoning here would break that parity (13 pairs in
// testdata/thinking_levels.json).
//
// Shaped after looksLikeVisionModel in vision_patterns.go, including the
// negative-first ordering.

// notTTSName matches ids that carry a speech word but synthesise nothing: the
// speech-to-text direction. Checked first so it can never match. "speech" alone
// is a TTS signal (minimax speech-2.8-hd), which is exactly why the recognition
// direction has to be excluded explicitly rather than by omission.
var notTTSName = regexp.MustCompile(`(?i)` + strings.Join([]string{
	`speech(` + nameSep + `|)to(` + nameSep + `|)text`,
	`text(` + nameSep + `|)to(` + nameSep + `|)speech(` + nameSep + `|)recognition`,
	`(^|` + nameSep + `)(stt|asr|whisper)(` + nameSep + `|$)`,
	`transcri`,
	`(^|` + nameSep + `)(voice(` + nameSep + `|)recognition)(` + nameSep + `|$)`,
}, "|"))

// ttsName matches the explicit synthesis words plus the model families whose
// ids name the architecture instead of the modality (nvidia fastpitch and
// tacotron2, the selfhosted kokoro).
var ttsName = regexp.MustCompile(`(?i)` + strings.Join([]string{
	`(^|` + nameSep + `)(tts|speech)(` + nameSep + `|$)`,
	`(^|` + nameSep + `)(tacotron[0-9]*|fastpitch|kokoro)(` + nameSep + `|$)`,
}, "|"))

// looksLikeTTSModel reports whether a model id looks like a speech-synthesis
// model by name signal alone.
func looksLikeTTSModel(modelID string) bool {
	if modelID == "" {
		return false
	}
	id := strings.ToLower(modelID)
	if notTTSName.MatchString(id) {
		return false
	}
	return ttsName.MatchString(id)
}
