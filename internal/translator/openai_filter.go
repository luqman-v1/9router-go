package translator

import (
	json "encoding/json/v2"
	"strings"
)

// OpenAI content/message block types missing from the package's existing
// sets (claude_blocks.go, responses_request.go), mirroring
// open-sse/translator/schema/blocks.js.
const (
	openaiBlockImage      = "image"
	openaiBlockInputAudio = "input_audio"
	openaiBlockAudioURL   = "audio_url"
	openaiBlockFile       = "file"

	claudeBlockRedactedThinking = "redacted_thinking"
	claudeBlockToolUse          = "tool_use"
)

// validOpenAIContentTypes mirrors VALID_OPENAI_CONTENT_TYPES.
var validOpenAIContentTypes = map[string]bool{
	openaiBlockText:       true,
	openaiBlockImageURL:   true,
	openaiBlockImage:      true,
	openaiBlockInputAudio: true,
	openaiBlockAudioURL:   true,
	openaiBlockFile:       true,
}

// FilterToOpenAIFormat normalises a body to clean OpenAI shape — port of
// filterToOpenAIFormat in open-sse/translator/formats/openai.js:10-133.
// Go declares no preserveCacheControl quirk, so signature and cache_control
// are always dropped from kept blocks.
func FilterToOpenAIFormat(body map[string]any) map[string]any {
	msgs, ok := body["messages"].([]any)
	if !ok {
		return body
	}
	out := make([]any, 0, len(msgs))
	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			out = append(out, m)
			continue
		}
		role, _ := msg["role"].(string)
		if role == "developer" {
			msg = shallowCopy(msg)
			msg["role"] = "system"
			role = "system"
		}
		if role == "tool" {
			out = append(out, msg)
			continue
		}
		if role == "assistant" {
			if _, has := msg["tool_calls"]; has {
				out = append(out, msg)
				continue
			}
		}
		if content, ok := msg["content"].(string); ok {
			_ = content
			out = append(out, msg)
			continue
		}
		if parts, ok := msg["content"].([]any); ok {
			filtered := make([]any, 0, len(parts))
			for _, p := range parts {
				block, ok := p.(map[string]any)
				if !ok {
					continue
				}
				typ, _ := block["type"].(string)
				switch {
				case typ == claudeBlockThinking || typ == claudeBlockRedactedThinking:
					continue
				case validOpenAIContentTypes[typ]:
					filtered = append(filtered, stripBlock(block))
				case typ == claudeBlockToolUse:
					continue
				case typ == claudeBlockToolResult:
					filtered = append(filtered, stripBlock(block))
				}
			}
			if len(filtered) == 0 {
				filtered = append(filtered, map[string]any{"type": openaiBlockText, "text": ""})
			}
			msg = shallowCopy(msg)
			msg["content"] = filtered
			out = append(out, msg)
			continue
		}
		out = append(out, msg)
	}
	// Drop messages with only empty text — never tool messages or
	// assistant-with-tool_calls.
	kept := out[:0]
	for _, m := range out {
		msg, ok := m.(map[string]any)
		if !ok {
			kept = append(kept, m)
			continue
		}
		role, _ := msg["role"].(string)
		if role == "tool" {
			kept = append(kept, m)
			continue
		}
		if role == "assistant" {
			if _, has := msg["tool_calls"]; has {
				kept = append(kept, m)
				continue
			}
		}
		switch content := msg["content"].(type) {
		case string:
			if strings.TrimSpace(content) != "" {
				kept = append(kept, m)
			}
		case []any:
			keep := false
			for _, p := range content {
				block, ok := p.(map[string]any)
				if !ok {
					continue
				}
				typ, _ := block["type"].(string)
				if typ == openaiBlockText {
					if s, _ := block["text"].(string); strings.TrimSpace(s) != "" {
						keep = true
						break
					}
				} else {
					keep = true
					break
				}
			}
			if keep {
				kept = append(kept, m)
			}
		default:
			kept = append(kept, m)
		}
	}
	body["messages"] = kept

	// Remove empty tools array (some providers reject it).
	if tools, ok := body["tools"].([]any); ok && len(tools) == 0 {
		delete(body, "tools")
	}
	// Normalise tools to OpenAI shape.
	if tools, ok := body["tools"].([]any); ok && len(tools) > 0 {
		var norm []any
		for _, t := range tools {
			tool, ok := t.(map[string]any)
			if !ok {
				norm = append(norm, t)
				continue
			}
			if tool["type"] == openaiBlockFunction {
				if _, has := tool["function"]; has {
					norm = append(norm, tool)
					continue
				}
			}
			if name, _ := tool["name"].(string); name != "" {
				if _, has := tool["input_schema"]; has {
					norm = append(norm, map[string]any{
						"type": openaiBlockFunction,
						"function": map[string]any{
							"name":        name,
							"description": stringField(tool, "description"),
							"parameters":  parametersField(tool, "input_schema"),
						},
					})
					continue
				}
				if _, has := tool["description"]; has {
					norm = append(norm, map[string]any{
						"type": openaiBlockFunction,
						"function": map[string]any{
							"name":        name,
							"description": stringField(tool, "description"),
							"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
						},
					})
					continue
				}
			}
			if decls, ok := tool["functionDeclarations"].([]any); ok {
				for _, d := range decls {
					fn, ok := d.(map[string]any)
					if !ok {
						continue
					}
					name, _ := fn["name"].(string)
					norm = append(norm, map[string]any{
						"type": openaiBlockFunction,
						"function": map[string]any{
							"name":        name,
							"description": stringField(fn, "description"),
							"parameters":  parametersField(fn, "parameters"),
						},
					})
				}
				continue
			}
			norm = append(norm, tool)
		}
		body["tools"] = norm
	}
	// Normalise tool_choice to OpenAI shape.
	if choice, ok := body["tool_choice"].(map[string]any); ok {
		typ, _ := choice["type"].(string)
		switch {
		case typ == "auto":
			body["tool_choice"] = "auto"
		case typ == "any":
			body["tool_choice"] = "required"
		case typ == "tool":
			if name, _ := choice["name"].(string); name != "" {
				body["tool_choice"] = map[string]any{
					"type":     openaiBlockFunction,
					"function": map[string]any{"name": name},
				}
			}
		}
	}
	return body
}

// FilterToOpenAIFormatJSON is FilterToOpenAIFormat over raw bytes;
// decode errors return the input unchanged.
func FilterToOpenAIFormatJSON(body []byte) []byte {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return body
	}
	FilterToOpenAIFormat(m)
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// stripBlock drops signature and cache_control from a kept block.
func stripBlock(block map[string]any) map[string]any {
	out := shallowCopy(block)
	delete(out, "signature")
	delete(out, "cache_control")
	return out
}

func shallowCopy(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func stringField(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func parametersField(m map[string]any, key string) any {
	if p, ok := m[key]; ok && p != nil {
		return p
	}
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
