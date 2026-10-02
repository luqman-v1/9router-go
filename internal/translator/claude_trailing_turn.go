package translator

import (
	json "encoding/json/v2"
	"strings"
)

// TrailingUserPlaceholder is the one-token user turn appended when cleanup
// leaves a Claude conversation ending on an assistant message. Upstream:
// TRAILING_USER_PLACEHOLDER in open-sse/translator/formats/claude.js.
const TrailingUserPlaceholder = "Continue."

// ClaudeIntentionalPrefill reports whether the client deliberately opened the
// conversation with an assistant turn, i.e. a real prefill rather than cleanup
// having emptied the trailing user turn. Upstream reads the role before any
// translation runs (detectClientLastRole, open-sse/translator/index.js:59-65)
// so it is taken from the source format's own shape: messages[] for a Claude
// client, contents[] for Gemini/Antigravity, input[] for Responses/Codex.
// Only an explicit trailing model/assistant turn counts; every other tail
// (including a function output, which carries no role at all) leaves the
// repair in force.
func ClaudeIntentionalPrefill(rawBody []byte) bool {
	var body map[string]any
	if err := json.Unmarshal(rawBody, &body); err != nil {
		return false
	}
	role := lastRole(body["messages"])
	if role == "" {
		items, ok := body["contents"].([]any)
		if !ok {
			items, ok = body["input"].([]any)
		}
		if !ok {
			return false
		}
		role = lastRole(items)
	}
	// "model" is the Gemini spelling of an assistant turn.
	return role == "assistant" || role == "model"
}

// EnsureTrailingUserTurn appends a placeholder user turn when messages ends on
// an assistant message and the client did not ask for that.
//
// Newer Claude models reject a body that ends on an assistant turn with 400
// "… does not support assistant message prefill". The cleanup passes in
// SanitizeClaudePassthrough delete messages left empty, so a trailing user
// turn that was blank (or held only dropped blocks) silently promotes the
// previous assistant turn to last. A prefill the client wrote itself is its
// choice and is left alone — upstream ensureTrailingUserTurn
// (open-sse/translator/formats/claude.js:345-349). A trailing user turn whose
// content the cleanup emptied is replaced by the placeholder instead of being
// dropped: an empty tail is rejected by Anthropic just as hard as a missing
// one.
func EnsureTrailingUserTurn(messages []any, intentionalPrefill bool) []any {
	if intentionalPrefill || len(messages) == 0 {
		return messages
	}
	last := messages[len(messages)-1]
	if lastRole(messages) != "assistant" {
		// The last user turn carries no usable content — it held only blocks the
		// cleanup dropped, or was blank to begin with. Anthropic rejects empty
		// content too, so it is replaced by the placeholder rather than sent as
		// is; a conversation that really ends on a user turn is untouched.
		if msg, ok := last.(map[string]any); !ok || hasContent(msg["content"]) {
			return messages
		}
		messages = append(messages[:len(messages)-1:len(messages)-1], last)
	}
	return append(messages, map[string]any{
		"role":    "user",
		"content": []any{map[string]any{"type": "text", "text": TrailingUserPlaceholder}},
	})
}

// hasContent reports whether a decoded message content field carries anything
// Anthropic will accept: a non-blank string, or a non-empty block array.
func hasContent(content any) bool {
	switch c := content.(type) {
	case string:
		return strings.TrimSpace(c) != ""
	case []any:
		return len(c) > 0
	case nil:
		return false
	}
	return true
}

// lastRole returns the role of the final entry of a decoded message list,
// tolerating a non-object entry.
func lastRole(items any) string {
	list, ok := items.([]any)
	if !ok || len(list) == 0 {
		return ""
	}
	obj, ok := list[len(list)-1].(map[string]any)
	if !ok {
		return ""
	}
	role, _ := obj["role"].(string)
	return strings.TrimSpace(role)
}

// EnsureTrailingUserTurnBody applies EnsureTrailingUserTurn to the messages of
// an encoded Claude Messages body, returning the caller's own bytes when the
// conversation already ends on a user turn.
func EnsureTrailingUserTurnBody(body []byte, intentionalPrefill bool) []byte {
	if intentionalPrefill {
		return body
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}
	messages, ok := req["messages"].([]any)
	if !ok {
		return body
	}
	repaired := EnsureTrailingUserTurn(messages, false)
	if len(repaired) == len(messages) {
		return body
	}
	req["messages"] = repaired
	out, err := json.Marshal(req)
	if err != nil {
		return body
	}
	return out
}

// isTrailingUserTurn reports whether the message at index is the last message
// of the conversation and carries the user role. That message must survive the
// empty-content filter so the tail can be repaired instead of leaving an
// assistant turn last.
func isTrailingUserTurn(messages []any, index int) bool {
	if index < 0 || index >= len(messages)-1 {
		return false
	}
	obj, ok := messages[index].(map[string]any)
	if !ok {
		return false
	}
	role, _ := obj["role"].(string)
	return role == "user"
}

// sameTail reports whether a repaired message list still ends on the same
// message it started with. The repair rewrites the emptied trailing turn in
// place, so the length does not change and only the tail tells.
func sameTail(repaired, original []any) bool {
	if len(repaired) == 0 || len(original) == 0 {
		return len(repaired) == len(original)
	}
	last := len(repaired) - 1
	a, aOK := repaired[last].(map[string]any)
	b, bOK := original[len(original)-1].(map[string]any)
	if !aOK || !bOK {
		return true
	}
	left, err := json.Marshal(a)
	if err != nil {
		return true
	}
	right, err := json.Marshal(b)
	if err != nil {
		return true
	}
	return string(left) == string(right)
}
