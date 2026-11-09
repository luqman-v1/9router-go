package translator

import (
	"encoding/json"
	"strings"
)

// Block types the Claude Messages API accepts in a message content array.
const (
	claudeBlockText       = "text"
	claudeBlockThinking   = "thinking"
	claudeBlockToolResult = "tool_result"
	// ContainerUpload references a file uploaded through the Anthropic Files
	// API (upstream #4316); it carries a file_id instead of inline bytes.
	claudeBlockContainerUpload = "container_upload"
)

// contentfulBlocks are the block types that count as a message having content.
// A message holding none of them is empty and gets dropped: Anthropic rejects
// empty content, and forwarding one is worse. Anything the caller can
// legitimately send ALONE must be listed — container_upload is exactly that:
// a user turn whose only block is a file reference is valid input, and leaving
// it out forwards `messages: []` to the provider.
var contentfulBlocks = map[string]struct{}{
	"tool_use":                 {},
	claudeBlockToolResult:      {},
	"image":                    {},
	"document":                 {},
	claudeBlockContainerUpload: {},
	"redacted_thinking":        {},
}

// isContentfulBlock reports whether one content block keeps its message alive.
// Text counts only when it is not blank: an Anthropic 400s on empty text, and
// a whitespace-only turn carries nothing either.
// Thinking blocks count only when carrying a valid Claude signature, ensuring
// invalid/unsigned thinking blocks are dropped while valid blank thinking survives.
func isContentfulBlock(block map[string]any) bool {
	bType, _ := block["type"].(string)
	if bType == claudeBlockText {
		text, _ := block["text"].(string)
		return strings.TrimSpace(text) != ""
	}
	if bType == claudeBlockThinking {
		sig, _ := block["signature"].(string)
		return IsValidClaudeSignature(sig)
	}
	_, ok := contentfulBlocks[bType]
	return ok
}

// isContentfulContent reports whether a message's `content` holds anything
// worth forwarding. Both wire shapes count: a bare string and a single content
// block object sent in place of the array.
func isContentfulContent(content any) bool {
	switch c := content.(type) {
	case string:
		return strings.TrimSpace(c) != ""
	case []any:
		for _, bRaw := range c {
			if block, ok := bRaw.(map[string]any); ok && isContentfulBlock(block) {
				return true
			}
		}
	case map[string]any:
		return isContentfulBlock(c)
	}
	return false
}

// markLastCacheableBlock puts a 5-minute ephemeral breakpoint on the last
// cache-eligible block of a message and reports whether one was found.
// thinking/redacted_thinking blocks do not accept cache_control.
func markLastCacheableBlock(content []any) bool {
	for i := len(content) - 1; i >= 0; i-- {
		block, ok := content[i].(map[string]any)
		if !ok {
			continue
		}
		if bType, _ := block["type"].(string); bType == claudeBlockThinking || bType == "redacted_thinking" {
			continue
		}
		block["cache_control"] = map[string]any{"type": "ephemeral"}
		return true
	}
	return false
}

// markFinalToolResults caches the tool loop's final tool results.
//
// A tool loop's request ends with the results of the last assistant turn's
// tool calls — after that turn's breakpoint — so they are billed as uncached
// input and only written to the cache by the *next* request, which appends to
// them. While the 4-marker budget has room, a 5m breakpoint on that final user
// turn caches them now and the next step reads them. A turn that already holds a
// marker is left alone, so re-anchoring stays idempotent.
func markFinalToolResults(req map[string]any) bool {
	messages, ok := req["messages"].([]any)
	if !ok || len(messages) == 0 {
		return false
	}
	last, ok := messages[len(messages)-1].(map[string]any)
	if !ok {
		return false
	}
	if role, _ := last["role"].(string); role != "user" {
		return false
	}
	content, ok := last["content"].([]any)
	if !ok {
		return false
	}
	hasResult, hasMarker := false, false
	for _, bRaw := range content {
		block, ok := bRaw.(map[string]any)
		if !ok {
			continue
		}
		if bType, _ := block["type"].(string); bType == claudeBlockToolResult {
			hasResult = true
		}
		if block["cache_control"] != nil {
			hasMarker = true
		}
	}
	if !hasResult || hasMarker {
		return false
	}
	if countCacheControlBlocks(req) >= cacheControlMarkerBudget {
		return false
	}
	return markLastCacheableBlock(content)
}

// ToolResultIsError inspects a tool turn or tool_result block/message map and
// reports whether it indicates an error condition.
// Zed Editor and strict Anthropic consumers require `is_error: bool` to be
// explicitly emitted on every tool_result block.
// Returns true when:
// - `is_error` / `isError` boolean is true
// - `status` equals "error" (case-insensitive)
// - content contains an error payload or JSON error structure
func ToolResultIsError(m map[string]any) bool {
	if m == nil {
		return false
	}
	if b, ok := m["is_error"].(bool); ok && b {
		return true
	}
	if b, ok := m["isError"].(bool); ok && b {
		return true
	}
	if s, ok := m["status"].(string); ok && strings.EqualFold(strings.TrimSpace(s), "error") {
		return true
	}

	// Check content/text fields for string error markers or JSON error payloads
	for _, key := range []string{"content", "output", "text"} {
		if raw, ok := m[key]; ok && raw != nil {
			if checkToolContentIsError(raw) {
				return true
			}
		}
	}
	return false
}

func checkToolContentIsError(content any) bool {
	switch v := content.(type) {
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return false
		}
		// Check JSON error payload
		if strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}") {
			var obj map[string]any
			if err := json.Unmarshal([]byte(trimmed), &obj); err == nil {
				if b, ok := obj["is_error"].(bool); ok && b {
					return true
				}
				if b, ok := obj["isError"].(bool); ok && b {
					return true
				}
				if s, ok := obj["status"].(string); ok && strings.EqualFold(strings.TrimSpace(s), "error") {
					return true
				}
				if errVal, hasErr := obj["error"]; hasErr && errVal != nil && errVal != false && errVal != "" {
					return true
				}
			}
		}
		// Error string markers
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "error:") ||
			strings.HasPrefix(lower, "exception:") ||
			strings.HasPrefix(lower, "traceback (most recent call last):") {
			return true
		}
	case []any:
		for _, item := range v {
			if im, ok := item.(map[string]any); ok {
				if ToolResultIsError(im) {
					return true
				}
			}
		}
	case map[string]any:
		return ToolResultIsError(v)
	}
	return false
}
