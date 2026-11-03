package executor

import (
	"bytes"
	json "encoding/json/v2"
	"strings"
)

// isComposerModel reports whether a model identifier represents a Cursor Composer model.
func isComposerModel(model string) bool {
	parts := strings.Split(model, "/")
	id := strings.ToLower(parts[len(parts)-1])
	return strings.HasPrefix(id, "composer")
}

// visibleComposerContentFromThinking extracts visible response content appearing after
// the last `</think>` closing tag in thinking blocks (upstream #1310 parity).
func visibleComposerContentFromThinking(thinking string) string {
	if thinking == "" {
		return ""
	}
	endTag := "</think>"
	idx := strings.LastIndex(thinking, endTag)
	if idx < 0 {
		return ""
	}
	return strings.TrimLeft(thinking[idx+len(endTag):], " \t\r\n")
}

// decodeComposerThinkingPayload inspects reasoning in chunks and emits visible content
// when model is a Composer model and thinking carries text after </think>.
func decodeComposerThinkingPayload(payload []byte) []byte {
	if !bytes.Contains(payload, []byte(`</think>`)) {
		return payload
	}
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
		rawThinking, ok := delta["thinking"].(string)
		if !ok || rawThinking == "" {
			if rc, ok := delta["reasoning_content"].(string); ok {
				rawThinking = rc
			}
		}
		if visible := visibleComposerContentFromThinking(rawThinking); visible != "" {
			existingContent, _ := delta["content"].(string)
			if existingContent == "" {
				delta["content"] = visible
				changed = true
			}
		}
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
