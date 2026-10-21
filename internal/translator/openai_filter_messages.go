package translator

import "strings"

// filterMessages normalises a message list to the OpenAI shape: the developer
// role becomes system, the thinking blocks are dropped, and the messages that
// carry nothing are pruned. Port of the message pass in upstream's
// filterToOpenAIFormat (open-sse/translator/formats/openai.js:19-73).
func filterMessages(msgs []any, opts FilterOptions) []any {
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
		// A tool turn keeps its own shape, and an assistant turn with tool
		// calls keeps its tool_calls — both are the OpenAI contract, and
		// upstream keeps them as-is for the same reason.
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
		if _, ok := msg["content"].(string); ok {
			out = append(out, msg)
			continue
		}
		if parts, ok := msg["content"].([]any); ok {
			msg = shallowCopy(msg)
			msg["content"] = filterContentBlocks(parts, opts)
			out = append(out, msg)
			continue
		}
		out = append(out, msg)
	}
	return pruneEmptyMessages(out)
}

// filterContentBlocks keeps the blocks an OpenAI provider accepts and drops
// the thinking ones. A message whose content was emptied carries an empty
// text block instead, so the turn stays valid.
func filterContentBlocks(parts []any, opts FilterOptions) []any {
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
			filtered = append(filtered, stripBlock(block, opts))
		case typ == claudeBlockToolUse:
			continue
		case typ == claudeBlockToolResult:
			filtered = append(filtered, stripBlock(block, opts))
		}
	}
	if len(filtered) == 0 {
		filtered = append(filtered, map[string]any{"type": openaiBlockText, "text": ""})
	}
	return filtered
}

// pruneEmptyMessages drops the turns that carry no content, but never a tool
// turn or an assistant turn with tool_calls.
func pruneEmptyMessages(msgs []any) []any {
	kept := msgs[:0]
	for _, m := range msgs {
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
	return kept
}

// normalizeTools converts the tool declarations to the OpenAI shape and drops
// an empty tool list, which some providers reject.
func normalizeTools(body map[string]any) {
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) == 0 {
		if ok {
			delete(body, "tools")
		}
		return
	}
	norm := make([]any, 0, len(tools))
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

// normalizeToolChoice converts the Claude tool_choice shape to the OpenAI one.
func normalizeToolChoice(body map[string]any) {
	choice, ok := body["tool_choice"].(map[string]any)
	if !ok {
		return
	}
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
