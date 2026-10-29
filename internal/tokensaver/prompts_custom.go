package tokensaver

import (
	json "encoding/json/v2"
	"strings"
)

// slot says which end of the system prompt a custom text block belongs to.
// The prefix is the first thing the model reads; the suffix is its last word,
// which is what makes it survive a turn of untrusted content.
type slot int

const (
	slotPrefix slot = iota
	slotSuffix
)

// InjectPrefixSystemPrompt puts text at the very front of the conversation's
// system prompt: the first system/developer turn of an OpenAI Chat messages[],
// the top-level "system" of a Claude Messages body, or the instructions /
// first developer item of a Responses body. Creates the system prompt when the
// body has none. Returns the body unchanged and false when there is nothing to
// inject into or the text is already there.
func InjectPrefixSystemPrompt(body []byte, text string, claudeNative bool) ([]byte, bool) {
	return injectCustomSlot(body, text, claudeNative, slotPrefix)
}

// InjectSuffixSystemPrompt puts text at the very end of the conversation's
// system prompt. Same wire contracts as InjectPrefixSystemPrompt; the suffix
// lands after the last system/developer turn instead of the first.
func InjectSuffixSystemPrompt(body []byte, text string, claudeNative bool) ([]byte, bool) {
	return injectCustomSlot(body, text, claudeNative, slotSuffix)
}

// injectCustomSlot dispatches on the body's own wire shape, so a caller that
// does not know the protocol still injects into the right field.
func injectCustomSlot(body []byte, text string, claudeNative bool, s slot) ([]byte, bool) {
	if strings.TrimSpace(text) == "" {
		return body, false
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body, false
	}
	if claudeNative || req["system"] != nil {
		if out, ok := injectClaudeSlot(req, text, s); ok {
			return out, true
		}
		return body, false
	}
	if req["instructions"] != nil || toMessageArray(req["input"]) != nil {
		if out, ok := injectResponsesSlot(req, text, s); ok {
			return out, true
		}
		return body, false
	}
	arr := toMessageArray(req["messages"])
	if arr == nil {
		return body, false
	}
	if out, ok := injectChatSlot(req, arr, text, s); ok {
		return out, true
	}
	return body, false
}

// joinCustom merges text into an existing system prompt string.
func joinCustom(current, text string, s slot) (string, bool) {
	if strings.Contains(current, text) {
		return "", false
	}
	switch {
	case current == "":
		return text, true
	case s == slotPrefix:
		return text + "\n\n" + current, true
	default:
		return current + "\n\n" + text, true
	}
}

// injectClaudeSlot writes the Anthropic top-level "system" field. The API
// rejects role:"system" inside messages[], so there is no array fallback here.
func injectClaudeSlot(req map[string]any, text string, s slot) ([]byte, bool) {
	switch sys := req["system"].(type) {
	case string:
		merged, ok := joinCustom(sys, text, s)
		if !ok {
			return nil, false
		}
		req["system"] = merged
	case []any:
		if claudeBlocksCarry(sys, text) {
			return nil, false
		}
		block := map[string]any{"type": "text", "text": text}
		if s == slotPrefix {
			req["system"] = append([]any{block}, sys...)
		} else {
			req["system"] = append(sys, block)
		}
	case nil:
		req["system"] = text
	default:
		return nil, false
	}
	return marshalBody(req)
}

func claudeBlocksCarry(blocks []any, text string) bool {
	for _, block := range blocks {
		bMap, ok := block.(map[string]any)
		if !ok {
			continue
		}
		if t, ok := bMap["text"].(string); ok && strings.Contains(t, text) {
			return true
		}
	}
	return false
}

// injectChatSlot writes an OpenAI Chat Completions messages[] turn. A turn
// whose content is a block array keeps that array and gets a separate system
// message instead — overwriting it would drop the original instructions.
func injectChatSlot(req map[string]any, arr []any, text string, s slot) ([]byte, bool) {
	clone := make([]any, len(arr))
	copy(clone, arr)

	target := -1
	for i, m := range clone {
		msg, ok := m.(map[string]any)
		if !ok || !isSystemRole(msg["role"]) {
			continue
		}
		if _, isString := msg["content"].(string); !isString {
			continue
		}
		target = i
		if s == slotPrefix {
			break // first turn wins for the prefix
		}
	}
	if target < 0 {
		if s == slotPrefix {
			req["messages"] = append([]any{map[string]any{"role": "system", "content": text}}, clone...)
		} else {
			req["messages"] = append(clone, map[string]any{"role": "system", "content": text})
		}
		return marshalBody(req)
	}

	msg := clone[target].(map[string]any)
	merged, ok := joinCustom(msg["content"].(string), text, s)
	if !ok {
		return nil, false
	}
	msg["content"] = merged
	clone[target] = msg
	req["messages"] = clone
	return marshalBody(req)
}

// injectResponsesSlot writes the OpenAI Responses system prompt: the top-level
// instructions string, or a system/developer item in input[] holding input_text
// blocks. A bare string content is upgraded to a block array — Codex rejects it
// as `input[0].content`.
func injectResponsesSlot(req map[string]any, text string, s slot) ([]byte, bool) {
	if instr, ok := req["instructions"].(string); ok {
		merged, keep := joinCustom(instr, text, s)
		if !keep {
			return nil, false
		}
		req["instructions"] = merged
		return marshalBody(req)
	}

	arr := toMessageArray(req["input"])
	if arr == nil {
		return nil, false
	}
	clone := make([]any, len(arr))
	copy(clone, arr)

	target := -1
	for i, item := range clone {
		itemMap, ok := item.(map[string]any)
		if !ok || responsesItemType(itemMap) != "message" || !isSystemRole(itemMap["role"]) {
			continue
		}
		target = i
		if s == slotPrefix {
			break
		}
	}
	if target < 0 {
		if s == slotPrefix {
			req["input"] = append([]any{newResponsesSystemItem(text)}, clone...)
		} else {
			req["input"] = append(clone, newResponsesSystemItem(text))
		}
		return marshalBody(req)
	}

	itemMap := clone[target].(map[string]any)
	if responsesContentHasPrompt(itemMap["content"], text) {
		return nil, false
	}
	if s == slotPrefix {
		itemMap["content"] = prependResponsesPrompt(itemMap["content"], text)
	} else {
		itemMap["content"] = appendResponsesPrompt(itemMap, text)["content"]
	}
	clone[target] = itemMap
	req["input"] = clone
	return marshalBody(req)
}

// prependResponsesPrompt adds the prompt as the first content block, accepting
// either shape the item may already carry.
func prependResponsesPrompt(content any, prompt string) any {
	block := map[string]any{"type": "input_text", "text": prompt}
	switch c := content.(type) {
	case string:
		return []any{block, map[string]any{"type": "input_text", "text": c}}
	case []any:
		return append([]any{block}, c...)
	default:
		return []any{block}
	}
}

func isSystemRole(role any) bool {
	r, _ := role.(string)
	return r == "system" || r == "developer"
}

func marshalBody(req map[string]any) ([]byte, bool) {
	out, err := json.Marshal(req, json.Deterministic(true))
	if err != nil {
		return nil, false
	}
	return out, true
}
