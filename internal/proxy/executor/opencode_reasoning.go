package executor

import (
	"bytes"
	json "encoding/json/v2"
	"strings"
)

// reasoningTextFromDeltaMap mirrors translator.extractReasoningText for the
// raw-map deltas the SSE relays and aggregator work with: reasoning_content,
// then reasoning, then reasoning_details[] (bare strings or text/content
// objects), joined with no separator.
func reasoningTextFromDeltaMap(delta map[string]any) string {
	if s, ok := delta["reasoning_content"].(string); ok && s != "" {
		return s
	}
	if s, ok := delta["reasoning"].(string); ok && s != "" {
		return s
	}
	details, ok := delta["reasoning_details"].([]any)
	if !ok {
		return ""
	}
	var b strings.Builder
	for _, d := range details {
		switch v := d.(type) {
		case string:
			b.WriteString(v)
		case map[string]any:
			if s, ok := v["text"].(string); ok && s != "" {
				b.WriteString(s)
			} else if s, ok := v["content"].(string); ok && s != "" {
				b.WriteString(s)
			}
		}
	}
	return b.String()
}

// liftReasoningAliasesPayload patches one OpenAI SSE data payload, adding the
// canonical reasoning_content field when an upstream streams the vendor
// aliases instead (OpenCode Free's MiMo route: reasoning + reasoning_details[]).
// Only ever adds — the aliases stay intact for clients that read them — and
// never touches a delta that already carries reasoning_content. Without this,
// OpenAI-shaped clients see no thinking at all: the aggregator and usage
// tracking read reasoning_content. Payloads without an alias field are
// returned untouched without parsing.
func liftReasoningAliasesPayload(payload []byte) []byte {
	if !bytes.Contains(payload, []byte(`"reasoning`)) {
		return payload
	}
	var chunk map[string]any
	if err := json.Unmarshal(payload, &chunk); err != nil {
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
		if s, ok := delta["reasoning_content"].(string); ok && s != "" {
			continue
		}
		if text := reasoningTextFromDeltaMap(delta); text != "" {
			delta["reasoning_content"] = text
			changed = true
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
