package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Claude OAuth tool-cloak response decloaking (mirrors decloakToolNames /
// decloakStreamChunk from the dashboard's open-sse/utils/claudeCloaking.js).
// The chat handler cloaks outgoing tool names for OAuth connections; these
// helpers restore the original names before responses reach the client.

// CCDecoyTools mirrors CC_DECOY_TOOLS from claudeCloaking.js: Claude Code
// tool names declared as unusable decoys for OAuth cloaking. The chat
// handler injects them into outgoing tool declarations; the decloaker
// below rewrites any response blocks that call them. Single source of
// truth — do not duplicate this list.
var CCDecoyTools = []string{
	"Task", "TaskOutput", "TaskStop", "TaskCreate", "TaskGet", "TaskUpdate",
	"TaskList", "Bash", "Glob", "Grep", "Read", "Edit", "Write",
	"NotebookEdit", "WebFetch", "WebSearch", "AskUserQuestion", "Skill",
	"EnterPlanMode", "ExitPlanMode",
}

var ccDecoySet = func() map[string]bool {
	m := make(map[string]bool, len(CCDecoyTools))
	for _, name := range CCDecoyTools {
		m[name] = true
	}
	return m
}()

func isDecoyTool(name string) bool {
	return ccDecoySet[name]
}

// DecloakClaudeResponseBody restores original tool names in a non-streaming
// Claude response body.
func DecloakClaudeResponseBody(body []byte, toolNameMap map[string]string) []byte {
	if len(toolNameMap) == 0 {
		return body
	}
	var resp map[string]any
	if err := json.Unmarshal(body, &resp); err != nil {
		return body
	}
	content, ok := resp["content"].([]any)
	if !ok {
		return body
	}
	hasValidTools := false
	sawDecoy := false
	changed := false
	for _, c := range content {
		block, ok := c.(map[string]any)
		if !ok || block["type"] != "tool_use" {
			continue
		}
		if name, ok := block["name"].(string); ok {
			if orig, found := toolNameMap[name]; found {
				block["name"] = orig
				hasValidTools = true
				changed = true
			} else if isDecoyTool(name) {
				block["type"] = "text"
				block["text"] = fmt.Sprintf("[Tool %s is unavailable]", name)
				delete(block, "name")
				delete(block, "input")
				delete(block, "id")
				sawDecoy = true
				changed = true
			} else {
				hasValidTools = true
			}
		} else {
			hasValidTools = true
		}
	}
	if !hasValidTools && sawDecoy && resp["stop_reason"] == "tool_use" {
		resp["stop_reason"] = "end_turn"
		changed = true
	}
	if !changed {
		return body
	}
	out, err := json.Marshal(resp)
	if err != nil {
		return body
	}
	return out
}

// ClaudeStreamDecloaker tracks per-stream decloaking state across SSE chunks.
type ClaudeStreamDecloaker struct {
	toolNameMap   map[string]string
	decoyBlocks   map[int]bool
	hasValidTools bool
	sawDecoy      bool
}

func NewClaudeStreamDecloaker(toolNameMap map[string]string) *ClaudeStreamDecloaker {
	if len(toolNameMap) == 0 {
		return nil
	}
	return &ClaudeStreamDecloaker{
		toolNameMap: toolNameMap,
		decoyBlocks: make(map[int]bool),
	}
}

// extractEventType parses the top-level "type" field value from an SSE JSON
// payload without allocating or unmarshaling the entire JSON object. Nested
// "type" keys (e.g. inside content_block or delta objects) are ignored by
// tracking brace depth.
func extractEventType(payload string) string {
	depth := 0
	inStr := false
	esc := false
	for i := 0; i < len(payload); i++ {
		c := payload[i]
		if esc {
			esc = false
			continue
		}
		if inStr {
			if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
			if depth == 1 && strings.HasPrefix(payload[i:], `"type":`) {
				v := strings.TrimLeft(payload[i+len(`"type":`):], " \t")
				if strings.HasPrefix(v, `"`) {
					v = v[1:]
					if end := strings.IndexByte(v, '"'); end >= 0 {
						return v[:end]
					}
				}
			}
		case '{':
			depth++
		case '}':
			depth--
		}
	}
	return ""
}

// SSEEvent is a single processed SSE event ready to forward.
type SSEEvent struct {
	Type    string // SSE event: header value; "" means a plain data line
	Payload []byte // bare JSON payload, no "data:" prefix
}

// Events processes a single SSE chunk, restoring original tool names and
// converting decoy tool calls into text blocks. Input may be a raw
// "data: {...}" line or the bare JSON payload (ScanStream strips the prefix).
// Returns the events to forward in order — the (possibly modified) original
// plus, for a converted decoy tool_use start, a synthetic text_delta carrying
// the "[Tool ... is unavailable]" notice. A suppressed decoy delta returns no
// events. The [DONE] sentinel passes through as a bare payload, never with a
// second "data:" prefix.
func (d *ClaudeStreamDecloaker) Events(chunk []byte) []SSEEvent {
	if d == nil || len(chunk) == 0 {
		return []SSEEvent{{Payload: chunk}}
	}
	s := strings.TrimSpace(string(chunk))
	payload := s
	if strings.HasPrefix(s, "data:") {
		payload = strings.TrimSpace(strings.TrimPrefix(s, "data:"))
	}
	if payload == "" {
		return nil
	}
	if payload == "[DONE]" {
		return []SSEEvent{{Payload: []byte(payload)}}
	}
	eventType := extractEventType(payload)
	if !bytes.Contains(chunk, []byte(`"tool_use"`)) &&
		!bytes.Contains(chunk, []byte(`"message_delta"`)) &&
		(len(d.decoyBlocks) == 0 || (!bytes.Contains(chunk, []byte(`"content_block_delta"`)) && !bytes.Contains(chunk, []byte(`"content_block_stop"`)))) {
		return []SSEEvent{{Type: eventType, Payload: []byte(payload)}}
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		return []SSEEvent{{Type: eventType, Payload: []byte(payload)}}
	}
	if t, ok := event["type"].(string); ok && t != "" {
		eventType = t
	}
	changed := false
	syntheticText := ""
	switch eventType {
	case "content_block_start":
		idxVal, _ := event["index"].(float64)
		blockIdx := int(idxVal)
		block, ok := event["content_block"].(map[string]any)
		if ok && block["type"] == "tool_use" {
			if name, ok := block["name"].(string); ok {
				if orig, found := d.toolNameMap[name]; found {
					block["name"] = orig
					d.hasValidTools = true
					changed = true
				} else if isDecoyTool(name) {
					d.decoyBlocks[blockIdx] = true
					d.sawDecoy = true
					block["type"] = "text"
					syntheticText = fmt.Sprintf("[Tool %s is unavailable]", name)
					delete(block, "name")
					delete(block, "input")
					delete(block, "id")
					changed = true
				} else {
					d.hasValidTools = true
				}
			} else {
				d.hasValidTools = true
			}
		}

	case "content_block_delta":
		idxVal, _ := event["index"].(float64)
		if d.decoyBlocks[int(idxVal)] {
			// Suppress input_json_delta for decoy tool
			return nil
		}

	case "content_block_stop":
		idxVal, _ := event["index"].(float64)
		if d.decoyBlocks[int(idxVal)] {
			delete(d.decoyBlocks, int(idxVal))
		}

	case "message_delta":
		if delta, ok := event["delta"].(map[string]any); ok {
			if stopReason, ok := delta["stop_reason"].(string); ok && stopReason == "tool_use" {
				if !d.hasValidTools && d.sawDecoy {
					delta["stop_reason"] = "end_turn"
					changed = true
				}
			}
		}
	}

	if !changed {
		return []SSEEvent{{Type: eventType, Payload: []byte(payload)}}
	}
	updated, err := json.Marshal(event)
	if err != nil {
		return []SSEEvent{{Type: eventType, Payload: []byte(payload)}}
	}
	events := []SSEEvent{{Type: eventType, Payload: updated}}
	if syntheticText != "" {
		events = append(events, SSEEvent{
			Type: "content_block_delta",
			Payload: []byte(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":%s}}`,
				int(event["index"].(float64)), mustJSONString(syntheticText))),
		})
	}
	return events
}

// mustJSONString marshals s as a JSON string; used for the synthetic decoy
// text so quoting/escapes are always valid. Falls back to a sanitized literal
// if marshal fails (it cannot for a plain Go string).
func mustJSONString(s string) string {
	out, err := json.Marshal(s)
	if err != nil {
		return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
	}
	return string(out)
}
