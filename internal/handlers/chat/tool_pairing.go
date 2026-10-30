package chat

import (
	json "9router/proxy/internal/fastjson"
)

// A trimmed conversation must not orphan a tool result.
//
// A tool result names the call that produced it, and upstream validates that
// pairing: a result whose call is missing from the request is refused
// ("tool result's tool id (...) not found"). Trimming drops turns from the
// middle, so it can drop the assistant turn that made a call while keeping
// the result — which shortens the conversation and breaks it at the same time.
//
// Both wire spellings are handled: the Anthropic tool_use/tool_result content
// blocks, and the OpenAI assistant tool_calls plus role:"tool" messages.

const (
	toolUseBlockType    = "tool_use"
	toolResultBlockType = "tool_result"
)

// toolCallIDs collects the ids a message contributes as a tool CALL: an
// Anthropic tool_use block's id, or an OpenAI assistant turn's tool_calls ids.
func toolCallIDs(msg map[string]any, into map[string]bool) {
	if blocks, ok := msg["content"].([]any); ok {
		for _, b := range blocks {
			bm, ok := b.(map[string]any)
			if !ok {
				continue
			}
			if typ, _ := bm["type"].(string); typ == toolUseBlockType {
				if id, _ := bm["id"].(string); id != "" {
					into[id] = true
				}
			}
		}
	}
	if calls, ok := msg["tool_calls"].([]any); ok {
		for _, c := range calls {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			if id, _ := cm["id"].(string); id != "" {
				into[id] = true
			}
		}
	}
}

// toolResultRef reports whether a message is a tool RESULT that names a tool
// call, and which id it names.
//
// Only the OpenAI spelling is a whole message: a role:"tool" message IS the
// result. The Anthropic spelling is a block inside a user message, which is
// handled block by block so a text block beside it survives.
func toolResultRef(msg map[string]any) (string, bool) {
	if role, _ := msg["role"].(string); role == "tool" {
		if id, _ := msg["tool_call_id"].(string); id != "" {
			return id, true
		}
		return "", false
	}
	return "", false
}

// dropOrphanedToolResults removes the tool results whose tool call is no
// longer in the conversation.
//
// The conversation is walked in order, so a result is only kept when its call
// has already been seen. A result naming a call made earlier in the same
// conversation survives; one whose call was trimmed away does not.
func dropOrphanedToolResults(messages []any) []any {
	if len(messages) == 0 {
		return messages
	}
	known := map[string]bool{}
	out := make([]any, 0, len(messages))
	for _, m := range messages {
		msg, ok := m.(map[string]any)
		if !ok {
			out = append(out, m)
			continue
		}
		if id, isResult := toolResultRef(msg); isResult && !known[id] {
			continue
		}
		if blocks, ok := msg["content"].([]any); ok {
			kept := make([]any, 0, len(blocks))
			dropped := false
			for _, b := range blocks {
				bm, ok := b.(map[string]any)
				if ok {
					if typ, _ := bm["type"].(string); typ == toolResultBlockType {
						if id, _ := bm["tool_use_id"].(string); id != "" && !known[id] {
							dropped = true
							continue
						}
					}
				}
				kept = append(kept, b)
			}
			if dropped {
				if len(kept) == 0 {
					continue
				}
				cloned := make(map[string]any, len(msg))
				for k, v := range msg {
					cloned[k] = v
				}
				cloned["content"] = kept
				out = append(out, cloned)
				toolCallIDs(cloned, known)
				continue
			}
		}
		out = append(out, msg)
		toolCallIDs(msg, known)
	}
	return out
}

// repairToolPairing removes the tool results whose call was trimmed away.
//
// The body is returned untouched when nothing is orphaned, so the common case
// pays a walk and no re-encode.
func repairToolPairing(body []byte) []byte {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}
	key, arr, ok := historyArray(req)
	if !ok || len(arr) == 0 {
		return body
	}
	repaired := dropOrphanedToolResults(arr)
	if len(repaired) == len(arr) {
		return body
	}
	req[key] = repaired
	out, err := json.Marshal(req)
	if err != nil {
		return body
	}
	return out
}
