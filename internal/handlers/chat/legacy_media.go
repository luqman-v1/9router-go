package chat

import (
	"strings"

	json "9router/proxy/internal/fastjson"

	"9router/proxy/internal/log"
)

// An image sent turns ago still rides in the history of every later request,
// but DetectRequiredCapabilities deliberately scans only the trailing user
// run — old media must not pin a text conversation to a vision model. The
// design assumes that old media is "stripped or placeholdered downstream";
// that stripping exists for a few built-in executors (claude, gemini web) but
// not for the generic openai-compatible passthrough, so the history went out
// verbatim and a text-only upstream answered 400
// `content[1].type has unsupported value "image_url"` with no fallback to
// fall back to (the request never looked like a vision request).
//
// maybeStripLegacyMedia is that stripping, at the handler layer: when the
// history carries media but NO candidate model can serve it, every media
// block before the trailing user run is replaced with a text placeholder.
// The trailing run is never touched — a media block in the current turn is a
// real question, and the capacity-adapter pool (which only the trailing scan
// can trigger) is what answers it.
//
// Media in old turns is already answered; dropping it costs history fidelity,
// not capability. Keeping it costs a guaranteed upstream rejection once the
// rotation lands on a text-only model — the one case where no member of the
// chain can ever succeed.

// legacyMediaPlaceholder replaces a stripped media block. It names the turn so
// a model reading the rebuilt history can still follow the conversation.
const legacyMediaPlaceholder = "[media from an earlier turn removed: the serving model does not accept this media type]"

// legacyMediaBlockTypes are the content-block types that carry input media,
// across the wire shapes that reach this package: OpenAI (image_url,
// input_image, input_audio, input_video, input_file), Claude (image,
// document, audio... spelled bare), Gemini-adjacent translators (audio_url,
// video_url, file), and Hermes (image, audio, video).
var legacyMediaBlockTypes = map[string]bool{
	"image_url":   true,
	"image":       true,
	"input_image": true,
	"audio":       true,
	"input_audio": true,
	"audio_url":   true,
	"video":       true,
	"input_video": true,
	"video_url":   true,
	"file":        true,
	"document":    true,
	"input_file":  true,
}

// maybeStripLegacyMediaBytes is maybeStripLegacyMedia for a raw body. The
// input is returned untouched unless a strip actually changed something.
func maybeStripLegacyMediaBytes(body []byte, candidates []string) []byte {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}
	cleaned, changed := maybeStripLegacyMedia(req, candidates)
	if !changed {
		return body
	}
	out, err := json.Marshal(cleaned)
	if err != nil {
		return body
	}
	return out
}

// maybeStripLegacyMedia strips unservable media from a request's old turns.
// It reports whether anything changed; the map is edited in place, so the
// returned value is the input one. candidates are "provider/model" entries —
// a combo's member list, or the single target with any adapter pool already
// prepended — because a pool model that satisfies the media makes the strip
// unnecessary: the chain can serve the history as-is, with the reorder logic
// floating the capable member forward.
func maybeStripLegacyMedia(req map[string]any, candidates []string) (map[string]any, bool) {
	history := detectHistoryMediaCaps(req)
	if len(history) == 0 {
		return req, false
	}
	hard := requiredHardCaps(history)
	for _, c := range candidates {
		if modelSatisfiesHard(c, hard) {
			return req, false
		}
	}
	changed := stripLegacyMediaInRequest(req)
	if changed {
		log.Info("chat", "legacy media stripped from history",
			"caps", keysString(history), "candidates", strings.Join(candidates, ","))
	}
	return req, changed
}

// detectHistoryMediaCaps scans the WHOLE conversation for media requirements,
// the history-wide counterpart of DetectRequiredCapabilities' trailing-only
// scan. It reads the same wire shapes so the two can never disagree about
// what counts as media.
func detectHistoryMediaCaps(req map[string]any) map[string]bool {
	required := make(map[string]bool)
	_, arr, ok := historyArray(req)
	if !ok {
		return required
	}
	for _, item := range arr {
		// OpenAI / Claude / Hermes / Ollama messages.
		scanMessage(item, required)
		// Gemini contents carry media under parts, not content.
		scanContentBlocks(itemParts(item), required)
	}
	return required
}

// trailingRunStart returns the index of the first message of the trailing
// user run — everything from there on is the current turn and keeps its
// media. It mirrors trailingUserItems' walk so detection and stripping agree
// on where the current turn begins.
func trailingRunStart(arr []any) int {
	i := len(arr) - 1
	for i >= 0 {
		role := ""
		if m, ok := arr[i].(map[string]any); ok {
			role, _ = m["role"].(string)
		}
		if isAssistantRole(role) {
			break
		}
		i--
	}
	return i + 1
}

// stripLegacyMediaInRequest replaces media blocks in every message BEFORE the
// trailing user run. It reports whether anything changed.
func stripLegacyMediaInRequest(req map[string]any) bool {
	_, arr, ok := historyArray(req)
	if !ok {
		return false
	}
	boundary := trailingRunStart(arr)
	changed := false
	for i := 0; i < boundary; i++ {
		if msg, ok := arr[i].(map[string]any); ok {
			if stripLegacyMediaFromMessage(msg) {
				changed = true
			}
		}
	}
	return changed
}

// stripLegacyMediaFromMessage removes one message's media carriers: the
// Ollama images array, the message-level media keys some clients use, and the
// media blocks inside content / parts. Media inside a plain string content
// (a data URI inline in text) is left alone — rewriting prose is not a
// replacement, and no executor is known to reject on it.
func stripLegacyMediaFromMessage(msg map[string]any) bool {
	changed := false

	// Ollama / Hermes message-level images array.
	if images, ok := msg["images"].([]any); ok && len(images) > 0 {
		delete(msg, "images")
		changed = true
	}

	// Message-level modality keys (Vercel AI SDK attachments, Hermes).
	for _, key := range []string{
		"image_url", "image", "audio_url", "audio", "video_url", "video",
		"experimental_attachments", "attachments",
	} {
		if _, ok := msg[key]; ok {
			delete(msg, key)
			changed = true
		}
	}

	for _, key := range []string{"content", "parts"} {
		blocks, ok := msg[key].([]any)
		if !ok {
			continue
		}
		replaced := make([]any, 0, len(blocks))
		replacedAny := false
		for _, b := range blocks {
			bm, isMap := b.(map[string]any)
			if isMap && isLegacyMediaBlock(bm) {
				replaced = append(replaced, map[string]any{"type": "text", "text": legacyMediaPlaceholder})
				replacedAny = true
				continue
			}
			replaced = append(replaced, b)
		}
		if replacedAny {
			msg[key] = replaced
			changed = true
		}
	}
	return changed
}

// isLegacyMediaBlock reports whether a content block carries input media.
// Gemini parts need no type: the payload key itself is the marker.
func isLegacyMediaBlock(b map[string]any) bool {
	if t, _ := b["type"].(string); legacyMediaBlockTypes[t] {
		return true
	}
	_, hasInline := b["inlineData"]
	_, hasFileData := b["fileData"]
	return hasInline || hasFileData
}
