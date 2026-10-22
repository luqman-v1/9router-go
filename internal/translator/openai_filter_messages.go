package translator

import "strings"

// filterMessages normalises a message list to the OpenAI shape: the developer
// role becomes system, the thinking blocks are dropped, and the messages that
// carry nothing are pruned. Port of the message pass in upstream's
// filterToOpenAIFormat (open-sse/translator/formats/openai.js:19-73).
//
// The bool reports whether the list changed; the original slice is returned
// unchanged when it did not, so the caller can keep the bytes it received.
func filterMessages(msgs []any, opts FilterOptions) ([]any, bool) {
	out := make([]any, 0, len(msgs))
	changed := false
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
			changed = true
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
			filtered, blocksChanged := filterContentBlocks(parts, opts)
			if blocksChanged {
				msg = shallowCopy(msg)
				msg["content"] = filtered
				changed = true
			}
			out = append(out, msg)
			continue
		}
		out = append(out, msg)
	}
	pruned, prunedChanged := pruneEmptyMessages(out)
	if prunedChanged {
		changed = true
	}
	if !changed {
		return msgs, false
	}
	return pruned, true
}

// filterContentBlocks keeps the blocks an OpenAI provider accepts and drops
// the thinking ones. A message whose content was emptied carries an empty
// text block instead, so the turn stays valid. The bool reports whether any
// block was dropped or rewritten.
func filterContentBlocks(parts []any, opts FilterOptions) ([]any, bool) {
	filtered := make([]any, 0, len(parts))
	changed := false
	for _, p := range parts {
		block, ok := p.(map[string]any)
		if !ok {
			changed = true
			continue
		}
		typ, _ := block["type"].(string)
		switch {
		case typ == claudeBlockThinking || typ == claudeBlockRedactedThinking:
			changed = true
			continue
		case validOpenAIContentTypes[typ]:
			stripped, strippedChanged := stripBlock(block, opts)
			if strippedChanged {
				changed = true
			}
			filtered = append(filtered, stripped)
		case typ == claudeBlockToolUse:
			changed = true
			continue
		case typ == claudeBlockToolResult:
			stripped, strippedChanged := stripBlock(block, opts)
			if strippedChanged {
				changed = true
			}
			filtered = append(filtered, stripped)
		default:
			changed = true
			continue
		}
	}
	if len(filtered) == 0 {
		filtered = append(filtered, map[string]any{"type": openaiBlockText, "text": ""})
		changed = true
	}
	return filtered, changed
}

// pruneEmptyMessages drops the turns that carry no content, but never a tool
// turn or an assistant turn with tool_calls. The bool reports whether a turn
// was dropped.
//
// A body whose every turn carried nothing ends up with an empty message list;
// the JSON entry point reports that as ErrNoMessages, so the gateway can
// answer the client itself instead of forwarding an empty messages array the
// upstream answers with a 400.
func pruneEmptyMessages(msgs []any) ([]any, bool) {
	kept := msgs[:0]
	changed := false
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
			} else {
				changed = true
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
			} else {
				changed = true
			}
		default:
			kept = append(kept, m)
		}
	}
	return kept, changed
}

// normalizeTools converts the tool declarations to the OpenAI shape and drops
// an empty tool list, which some providers reject. The bool reports whether
// the tool list changed.
func normalizeTools(body map[string]any) bool {
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) == 0 {
		if ok {
			delete(body, "tools")
			return true
		}
		return false
	}
	norm := make([]any, 0, len(tools))
	changed := false
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
				changed = true
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
				changed = true
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
			changed = true
			continue
		}
		norm = append(norm, tool)
	}
	if !changed {
		return false
	}
	body["tools"] = norm
	return true
}

// normalizeToolChoice converts the Claude tool_choice shape to the OpenAI one.
// The bool reports whether the choice changed.
func normalizeToolChoice(body map[string]any) bool {
	choice, ok := body["tool_choice"].(map[string]any)
	if !ok {
		return false
	}
	typ, _ := choice["type"].(string)
	switch typ {
	case "auto":
		body["tool_choice"] = "auto"
		return true
	case "any":
		body["tool_choice"] = "required"
		return true
	case "tool":
		if name, _ := choice["name"].(string); name != "" {
			body["tool_choice"] = map[string]any{
				"type":     openaiBlockFunction,
				"function": map[string]any{"name": name},
			}
			return true
		}
	}
	return false
}
