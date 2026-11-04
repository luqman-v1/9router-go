package executor

import (
	json "encoding/json/v2"
	"strings"
)

// isComposerModel reports whether a model identifier represents a Cursor Composer model.
func isComposerModel(model string) bool {
	parts := strings.Split(model, "/")
	id := strings.ToLower(parts[len(parts)-1])
	return strings.HasPrefix(id, "composer")
}

const composerThinkClose = "</think>"

// thinkClosePrefixTail returns the longest suffix of s that is a proper prefix
// of the  closing tag (length 0..7). A  tag can be split across two SSE
// chunks (e.g. `</th` + `ink>`), so the tail of a chunk that looks like the
// start of a closing tag must be carried to the next chunk instead of being
// dropped. Only the tail needs retaining: anything earlier cannot combine with
// future bytes to form the tag, so memory stays bounded.
func thinkClosePrefixTail(s string) string {
	tag := composerThinkClose
	for n := len(tag) - 1; n >= 1; n-- {
		if strings.HasSuffix(s, tag[:n]) {
			return s[len(s)-n:]
		}
	}
	return ""
}

// visibleComposerContentFromThinking extracts visible response content appearing
// after the last  closing tag in a single thinking block (upstream #1310 parity).
func visibleComposerContentFromThinking(thinking string) string {
	if thinking == "" {
		return ""
	}
	idx := strings.LastIndex(thinking, composerThinkClose)
	if idx < 0 {
		return ""
	}
	return strings.TrimLeft(thinking[idx+len(composerThinkClose):], " \t\r\n")
}

// composerThinkingDecoder decodes Cursor Composer visible output across SSE
// chunks. It is stateful: visible text often continues in subsequent chunks'
// thinking delta after , and the closing tag itself may be split across
// chunk boundaries.
type composerThinkingDecoder struct {
	pending string // carried tail: either a partial  tag (<=7 chars) or, once visible, trailing partial-tag bytes
	seenTag bool   // whether a  closing tag has been observed
}

// extractVisible returns the visible text newly revealed in this thinking
// chunk, updating the decoder state. Everything after the last  closing tag
// in the concatenated (carried + current) stream is visible.
func (d *composerThinkingDecoder) extractVisible(thinking string) string {
	s := d.pending + thinking
	d.pending = ""
	tag := composerThinkClose
	idx := strings.LastIndex(s, tag)
	if idx < 0 {
		if d.seenTag {
			// already visible: everything is visible, but a trailing partial
			// closing tag must be carried to the next chunk, not emitted.
			d.pending = thinkClosePrefixTail(s)
			return s[:len(s)-len(d.pending)]
		}
		// still in private reasoning: carry only a possible partial tag tail.
		d.pending = thinkClosePrefixTail(s)
		return ""
	}
	d.seenTag = true
	after := s[idx+len(tag):]
	d.pending = thinkClosePrefixTail(after)
	return after[:len(after)-len(d.pending)]
}

// Process rewrites one SSE data payload for a Cursor Composer stream, emitting
// visible answer text (appearing after the last ) into delta.content.
// Existing delta.content is preserved and the visible text is concatenated to
// it instead of being dropped. Safe to call repeatedly across chunks of one
// stream; state carries between calls.
func (d *composerThinkingDecoder) Process(payload []byte) []byte {
	var chunk map[string]any
	if err := json.Unmarshal(payload, &chunk); err != nil {
		return payload
	}
	model, _ := chunk["model"].(string)
	if !isComposerModel(model) {
		return payload
	}
	choices, _ := chunk["choices"].([]any)
	changed := false
	for _, c := range choices {
		choice, ok := c.(map[string]any)
		if !ok {
			continue
		}
		delta, ok := choice["delta"].(map[string]any)
		if !ok {
			continue
		}
		rawThinking, _ := delta["thinking"].(string)
		if rawThinking == "" {
			if rc, ok := delta["reasoning_content"].(string); ok {
				rawThinking = rc
			}
		}
		if rawThinking == "" && !d.seenTag && d.pending == "" {
			continue
		}
		visible := d.extractVisible(rawThinking)
		if visible == "" {
			continue
		}
		existingContent, _ := delta["content"].(string)
		delta["content"] = existingContent + visible
		changed = true
	}
	if !changed {
		return payload
	}
	out, err := json.Marshal(chunk)
	if err != nil {
		return payload
	}
	return out
}
