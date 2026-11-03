package proxy

import (
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"time"
)

// StreamToolCallIDFill repairs streamed OpenAI chunks whose tool-call
// fragments open a new index without an id. The OpenAI streaming contract
// puts the id and function.name on the first fragment of a tool call and only
// index + argument deltas on the rest; some providers open a brand-new index
// in a late fragment and omit the id entirely, and strict OpenAI-compatible
// clients answer with "Expected 'id' to be a string" and abort the whole
// turn. The id is remembered per tool-call index so a later fragment for the
// same index reuses it. Upstream parity: decolua/9router 95c9d03e
// (ensureStreamToolCallIds).
type StreamToolCallIDFill struct {
	seen map[int]string
	// next keys the no-index fragments, which upstream tracks as a JS Map
	// under the `undefined` key.
	next int
}

// NewStreamToolCallIDFill starts a per-stream fill state. One instance per
// client stream: ids must stay stable for the life of the stream, not the
// process.
func NewStreamToolCallIDFill() *StreamToolCallIDFill {
	return &StreamToolCallIDFill{seen: map[int]string{}}
}

const streamToolCallNoIndex = -1

// isNonEmptyToolCallDelta reports whether a tool-call delta carries client-meaningful
// data (id, function name, arguments fragment), non-function tool types, or provider
// extensions. Known empty function-call placeholders are dropped before allocating ids.
func isNonEmptyToolCallDelta(tc map[string]any) bool {
	if t, ok := tc["type"].(string); ok && t != "" && t != "function" {
		return true
	}
	for k := range tc {
		if k != "index" && k != "id" && k != "type" && k != "function" {
			return true
		}
	}
	fn, _ := tc["function"].(map[string]any)
	for k := range fn {
		if k != "name" && k != "arguments" {
			return true
		}
	}
	if id, ok := tc["id"].(string); ok && id != "" {
		return true
	}
	if fn != nil {
		if name, ok := fn["name"].(string); ok && name != "" {
			return true
		}
		if args, ok := fn["arguments"].(string); ok && args != "" {
			return true
		}
	}
	return false
}

// fill patches one OpenAI chat-completion data payload, returning the new
// payload and whether it changed. Payloads without "tool_calls" skip parsing
// entirely, so plain content chunks keep their zero-copy path; a payload that
// fails to parse is relayed untouched.
func (f *StreamToolCallIDFill) Fill(payload []byte) ([]byte, bool) {
	if !bytes.Contains(payload, []byte(`"tool_calls"`)) {
		return payload, false
	}
	var chunk map[string]any
	if err := json.Unmarshal(payload, &chunk); err != nil {
		return payload, false
	}
	changed := false
	choices, _ := chunk["choices"].([]any)
	for _, c := range choices {
		choice, ok := c.(map[string]any)
		if !ok {
			continue
		}
		delta, ok := choice["delta"].(map[string]any)
		if !ok {
			continue
		}
		calls, ok := delta["tool_calls"].([]any)
		if !ok {
			continue
		}
		nonEmpty := make([]any, 0, len(calls))
		for _, raw := range calls {
			tc, ok := raw.(map[string]any)
			if !ok || isNonEmptyToolCallDelta(tc) {
				nonEmpty = append(nonEmpty, raw)
			}
		}
		if len(nonEmpty) != len(calls) {
			changed = true
			if len(nonEmpty) == 0 {
				delete(delta, "tool_calls")
				continue
			}
			delta["tool_calls"] = nonEmpty
			calls = nonEmpty
		}
		for _, raw := range calls {
			tc, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			idx, hasIdx := streamToolCallIndex(tc)
			if id, ok := tc["id"].(string); ok && id != "" {
				if hasIdx {
					f.seen[idx] = id
				}
				continue
			}
			key := streamToolCallNoIndex
			if hasIdx {
				key = idx
			}
			id, known := f.seen[key]
			if !known {
				if hasIdx {
					id = fmt.Sprintf("call_%d_%d", idx, time.Now().UnixMilli())
				} else {
					id = fmt.Sprintf("call_%d_%d", f.next, time.Now().UnixMilli())
					f.next++
				}
				f.seen[key] = id
			}
			tc["id"] = id
			changed = true
		}
	}
	if !changed {
		return payload, false
	}
	out, err := json.Marshal(chunk)
	if err != nil {
		return payload, false
	}
	return out, true
}

func streamToolCallIndex(tc map[string]any) (int, bool) {
	switch v := tc["index"].(type) {
	case float64:
		return int(v), true
	case string:
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n, true
		}
	}
	return 0, false
}
