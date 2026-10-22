package executor

import (
	json "encoding/json/v2"
	"fmt"
	"strings"
)

// commandcodeFlattenText mirrors upstream flattenText
// (open-sse/translator/request/openai-to-commandcode.js:19): OpenAI content
// arrays collapse into one string, blank parts are dropped, and any other
// non-string value is stringified rather than dropped.
func commandcodeFlattenText(content any) string {
	switch c := content.(type) {
	case nil:
		return ""
	case string:
		return c
	case []any:
		parts := make([]string, 0, len(c))
		for _, p := range c {
			switch v := p.(type) {
			case string:
				parts = append(parts, v)
			case map[string]any:
				if txt, ok := v["text"].(string); ok {
					parts = append(parts, txt)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return fmt.Sprintf("%v", content)
	}
}

// commandcodeUserContentBlocks mirrors upstream toContentBlocks
// (open-sse/translator/request/openai-to-commandcode.js:75): CommandCode never
// accepts a raw string as content, so a user turn is always a block array and
// nil/empty/unusable content becomes one empty text block.
func commandcodeUserContentBlocks(content any) []any {
	switch c := content.(type) {
	case nil:
		return []any{map[string]any{"type": "text", "text": ""}}
	case string:
		return []any{map[string]any{"type": "text", "text": c}}
	case []any:
		blocks := make([]any, 0, len(c))
		for _, part := range c {
			switch p := part.(type) {
			case string:
				blocks = append(blocks, map[string]any{"type": "text", "text": p})
			case map[string]any:
				if pType, _ := p["type"].(string); pType == "text" {
					if txt, ok := p["text"].(string); ok {
						blocks = append(blocks, map[string]any{"type": "text", "text": txt})
						continue
					}
				}
				if img := toCommandcodeImageBlock(p); img != nil {
					blocks = append(blocks, img)
					continue
				}
				if txt, ok := p["text"].(string); ok {
					blocks = append(blocks, map[string]any{"type": "text", "text": txt})
				}
			}
		}
		if len(blocks) > 0 {
			return blocks
		}
		return []any{map[string]any{"type": "text", "text": ""}}
	default:
		return []any{map[string]any{"type": "text", "text": fmt.Sprintf("%v", content)}}
	}
}

// commandcodeParseToolArguments mirrors upstream safeParseJson
// (open-sse/translator/request/openai-to-commandcode.js:100): a JSON string
// becomes the parsed object, a non-string value passes through, and a nil or
// unparseable value becomes {} so input never serialises as null.
func commandcodeParseToolArguments(v any) any {
	if v == nil {
		return map[string]any{}
	}
	s, ok := v.(string)
	if !ok {
		return v
	}
	var parsed any
	if err := json.Unmarshal([]byte(s), &parsed); err != nil {
		return map[string]any{}
	}
	if parsed == nil {
		return map[string]any{}
	}
	return parsed
}

// commandcodeToolResultMessage converts an OpenAI role:"tool" turn into the
// tool-result block the provider accepts, on a role:"tool" message.
//
// The provider validates params.messages[*] against a union of
//
//	{role: "user"|"assistant", content: string | <anthropic-style blocks>}
//	{role: "tool",              content: [{type:"tool-result", toolCallId, toolName, output}]}
//
// so a tool-result block is only valid under role "tool". Verified live
// 2026-10-12 against provider commandcode: with role "user" the request answered
// 400 with, per tool turn,
//
//	Invalid input: expected string, received array at "params.messages[N].content"
//
// and, on the tool branch, the only remaining complaint
//
//	Invalid input: expected "tool" at "params.messages[N].role"
//
// while the tool-result fields themselves validated. Upstream parity:
// openai-to-commandcode.js:120-129 emits role ROLE.TOOL with this exact block.
//
// Superseded (2026-05-07): this used to emit role "user" on the strength of a
// curl probe that read the schema header's "tool_result blocks (role=user)"
// note as proof that "tool" is rejected. That read one union branch as the whole
// schema; every tool turn was rejected as a result.
func commandcodeToolResultMessage(msg map[string]any) map[string]any {
	toolCallID, _ := msg["tool_call_id"].(string)
	toolName, _ := msg["name"].(string)
	return map[string]any{
		"role": "tool",
		"content": []any{map[string]any{
			"type":       "tool-result",
			"toolCallId": toolCallID,
			"toolName":   toolName,
			"output": map[string]any{
				"type":  "text",
				"value": commandcodeFlattenText(msg["content"]),
			},
		}},
	}
}

// commandcodeAssistantBlocks mirrors upstream convertMessages' assistant branch
// (openai-to-commandcode.js:130-155): empty content with no tool calls falls back
// to one empty text block (JS:153); reasoning block (emitted when there is
// reasoning text OR tool calls, using " " as the placeholder), then text, then
// one tool-call block per OpenAI tool_call with parsed arguments.
func commandcodeAssistantBlocks(msg map[string]any) []any {
	reasoning, _ := msg["reasoning_content"].(string)
	if reasoning == "" {
		reasoning, _ = msg["thought"].(string)
	}
	if reasoning == "" {
		reasoning, _ = msg["reasoning"].(string)
	}
	toolCalls, _ := msg["tool_calls"].([]any)

	blocks := make([]any, 0, len(toolCalls)+2)
	if reasoning != "" || len(toolCalls) > 0 {
		if reasoning == "" {
			reasoning = " "
		}
		blocks = append(blocks, map[string]any{"type": "reasoning", "text": reasoning})
	}
	if text := commandcodeFlattenText(msg["content"]); text != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": text})
	}
	for _, raw := range toolCalls {
		call, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		toolCallID, _ := call["id"].(string)
		toolName, _ := call["name"].(string)
		fn, _ := call["function"].(map[string]any)
		if toolName == "" && fn != nil {
			toolName, _ = fn["name"].(string)
		}
		if toolCallID == "" && fn != nil {
			toolCallID, _ = fn["id"].(string)
		}
		blocks = append(blocks, map[string]any{
			"type":       "tool-call",
			"toolCallId": toolCallID,
			"toolName":   toolName,
			"input":      commandcodeParseToolArguments(fn["arguments"]),
		})
	}
	if len(blocks) == 0 {
		blocks = append(blocks, map[string]any{"type": "text", "text": ""})
	}
	return blocks
}

// commandcodeConvertMessages mirrors upstream convertMessages
// (open-sse/translator/request/openai-to-commandcode.js:106). Returns the
// converted messages plus the joined system prompt ("" when there is none).
// Deliberate local divergence from upstream: a developer role feeds the system
// prompt instead of being downgraded to a user turn.
func commandcodeConvertMessages(rawMsgs []any) (messages []any, system string) {
	converted := make([]any, 0, len(rawMsgs))
	systemTexts := make([]string, 0, 2)
	for _, raw := range rawMsgs {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		switch role {
		case "system", "developer":
			if text := commandcodeFlattenText(msg["content"]); text != "" {
				systemTexts = append(systemTexts, text)
			}
		case "tool":
			converted = append(converted, commandcodeToolResultMessage(msg))
		case "assistant":
			converted = append(converted, map[string]any{
				"role":    "assistant",
				"content": commandcodeAssistantBlocks(msg),
			})
		default:
			converted = append(converted, map[string]any{
				"role":    "user",
				"content": commandcodeUserContentBlocks(msg["content"]),
			})
		}
	}
	return converted, strings.Join(systemTexts, "\n\n")
}

// commandcodeMaxTokens mirrors upstream `body.max_tokens ?? body.max_output_tokens
// ?? DEFAULT_MAX_TOKENS`. The fallback stays 1024 (upstream 64000): the free
// tier the provider bills against rejects a larger budget. A null value is an
// absent value, the way upstream's nullish coalescing reads it.
func commandcodeMaxTokens(body map[string]any) any {
	if v, ok := body["max_tokens"]; ok && v != nil {
		return v
	}
	if v, ok := body["max_output_tokens"]; ok && v != nil {
		return v
	}
	return 1024
}

// commandcodeTools projects the tool declarations onto the three keys the
// CommandCode schema takes. Upstream's convertTools emits exactly
// {name, description, input_schema}, and a Claude-shaped tool carries
// cache_control markers and a strict flag the schema does not define.
func commandcodeTools(tools []any) []any {
	out := make([]any, 0, len(tools))
	for _, t := range tools {
		m, ok := t.(map[string]any)
		if !ok {
			out = append(out, t)
			continue
		}
		name, hasName := m["name"]
		if !hasName {
			out = append(out, t)
			continue
		}
		projected := map[string]any{"name": name}
		if desc, ok := m["description"]; ok {
			projected["description"] = desc
		}
		if schema, ok := m["input_schema"]; ok {
			projected["input_schema"] = schema
		}
		out = append(out, projected)
	}
	return out
}
