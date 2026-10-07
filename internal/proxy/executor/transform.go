package executor

import (
	"9router/proxy/internal/log"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"9router/proxy/internal/providers"
)

// ExtractSimpleText extracts text from jsontext.Value (string or array[text]).
func ExtractSimpleText(raw jsontext.Value) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []map[string]any
	if err := json.Unmarshal(raw, &blocks); err == nil {
		for _, b := range blocks {
			if t, ok := b["text"].(string); ok && t != "" {
				return t
			}
		}
	}
	return ""
}

// convertUserContent converts OpenAI user message content to Responses API format.
func convertUserContent(raw jsontext.Value) map[string]any {
	result := map[string]any{
		"type": "message",
		"role": "user",
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil && text != "" {
		result["content"] = []map[string]any{
			{"type": "input_text", "text": text},
		}
		return result
	}

	var blocks []map[string]any
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var content []map[string]any
		for _, b := range blocks {
			if t, ok := b["text"].(string); ok && t != "" {
				content = append(content, map[string]any{
					"type": "input_text",
					"text": t,
				})
			}
			if img, ok := b["image_url"].(map[string]any); ok {
				if url, ok := img["url"].(string); ok {
					content = append(content, map[string]any{
						"type":      "input_image",
						"image_url": url,
					})
				}
			}
		}
		if len(content) > 0 {
			result["content"] = content
		} else {
			result["content"] = []map[string]any{
				{"type": "input_text", "text": "..."},
			}
		}
		return result
	}

	result["content"] = []map[string]any{
		{"type": "input_text", "text": "..."},
	}
	return result
}

// cleanResponsesModel strips the request modifiers a 9router client may wrap a
// model id in: the opencode provider prefix, and the "(level)" thinking
// override. Neither is part of the wire id.
func cleanResponsesModel(model string) string {
	clean := strings.TrimPrefix(model, "oc/")
	clean = strings.TrimPrefix(clean, "opencode/")
	if idx := strings.IndexByte(clean, '('); idx != -1 {
		clean = clean[:idx]
	}
	return clean
}

// codexUpstreamModel resolves a codex catalog id to the id the ChatGPT backend
// expects. The catalog publishes ids that are not wire ids — the `[1m]`
// extended-context variants and the synthesized `-review` ones — and the
// backend answers 400 when those reach it verbatim. It runs after
// cleanResponsesModel, so the "(level)" suffix is already gone here.
func codexUpstreamModel(model string) string {
	return providers.CodexUpstreamModelID(model)
}

// rewriteCodexUpstreamModel replaces the body's `model` with the id the codex
// backend expects. A body the gateway does not shape (unparseable, or one with
// no model field) is returned untouched rather than replaced with a guess.
func rewriteCodexUpstreamModel(body []byte) []byte {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	model, ok := m["model"].(string)
	if !ok || model == "" {
		return body
	}
	upstream := codexUpstreamModel(model)
	if upstream == model {
		return body
	}
	m["model"] = upstream
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// maxCallIDLen clamps Responses API call_id values; longer IDs are truncated.
const maxCallIDLen = 64

func clampCallID(id string) string {
	if len(id) > maxCallIDLen {
		return id[:maxCallIDLen]
	}
	return id
}

// buildResponsesBody transforms OpenAI Chat Completions body → Responses API body.
// Returns (responsesBody, modelName, error).
func buildResponsesBody(body []byte) ([]byte, string, error) {
	// If the body is already in Responses API format (has input[]), normalize fields and return
	var quickCheck struct {
		Input               any    `json:"input"`
		Model               string `json:"model"`
		MaxTokens           *int   `json:"max_tokens,omitempty"`
		MaxCompletionTokens *int   `json:"max_completion_tokens,omitempty"`
		MaxOutputTokens     *int   `json:"max_output_tokens,omitempty"`
	}
	if err := json.Unmarshal(body, &quickCheck); err == nil && quickCheck.Input != nil {
		var m map[string]any
		if err := json.Unmarshal(body, &m); err == nil {
			cleanModel := cleanResponsesModel(quickCheck.Model)
			m["model"] = cleanModel
			m["stream"] = true
			m["store"] = false
			if m["max_output_tokens"] == nil {
				if quickCheck.MaxCompletionTokens != nil {
					m["max_output_tokens"] = *quickCheck.MaxCompletionTokens
				} else if quickCheck.MaxTokens != nil {
					m["max_output_tokens"] = *quickCheck.MaxTokens
				}
			}
			if mot, ok := m["max_output_tokens"].(float64); ok && mot < 16 && mot > 0 {
				m["max_output_tokens"] = 16
			} else if mot, ok := m["max_output_tokens"].(int); ok && mot < 16 && mot > 0 {
				m["max_output_tokens"] = 16
			}
			delete(m, "max_tokens")
			delete(m, "max_completion_tokens")

			// Normalize input if string or empty array (OpenAI Responses API parity)
			switch in := quickCheck.Input.(type) {
			case string:
				txt := strings.TrimSpace(in)
				if txt == "" {
					txt = "..."
				}
				m["input"] = []any{
					map[string]any{
						"type": "message",
						"role": "user",
						"content": []any{
							map[string]any{
								"type": "input_text",
								"text": txt,
							},
						},
					},
				}
			case []any:
				if len(in) == 0 {
					m["input"] = []any{
						map[string]any{
							"type": "message",
							"role": "user",
							"content": []any{
								map[string]any{
									"type": "input_text",
									"text": "...",
								},
							},
						},
					}
				}
			}

			// PR #4090: Repair missing call_id and clamp call_id in existing input items
			if inList, ok := m["input"].([]any); ok {
				var pendingCallIDs []string
				toolSeq := 0
				for _, item := range inList {
					if itemMap, ok := item.(map[string]any); ok {
						itemType, _ := itemMap["type"].(string)
						switch itemType {
						case "function_call", "custom_tool_call":
							cid, _ := itemMap["call_id"].(string)
							if cid == "" {
								cid = fmt.Sprintf("call_%d", toolSeq)
								toolSeq++
								itemMap["call_id"] = cid
							}
							if len(cid) > maxCallIDLen {
								cid = cid[:maxCallIDLen]
								itemMap["call_id"] = cid
							}
							pendingCallIDs = append(pendingCallIDs, cid)
						case "function_call_output", "custom_tool_call_output":
							cid, _ := itemMap["call_id"].(string)
							if cid != "" {
								for idx, p := range pendingCallIDs {
									if p == cid {
										pendingCallIDs = slices.Delete(pendingCallIDs, idx, idx+1)
										break
									}
								}
							} else if len(pendingCallIDs) > 0 {
								cid = pendingCallIDs[0]
								pendingCallIDs = pendingCallIDs[1:]
								itemMap["call_id"] = cid
							} else {
								cid = fmt.Sprintf("call_%d", toolSeq)
								toolSeq++
								itemMap["call_id"] = cid
							}
							if len(cid) > maxCallIDLen {
								itemMap["call_id"] = cid[:maxCallIDLen]
							}
						}
					}
				}
			}

			// Strip Unicode property escapes from existing tools parameters (Codex /responses validator parity #3922)
			if tools, ok := m["tools"].([]any); ok {
				for _, t := range tools {
					if tMap, ok := t.(map[string]any); ok {
						if params, ok := tMap["parameters"].(map[string]any); ok {
							tMap["parameters"] = StripCodexUnsupportedPatterns(params)
						}
					}
				}
			}

			applyCodexModelShape(m, cleanModel)
			out, err := json.Marshal(m)
			return out, cleanModel, err
		}
	}

	var oreq struct {
		Model               string         `json:"model"`
		Messages            jsontext.Value `json:"messages"`
		Instructions        string         `json:"instructions,omitempty"`
		MaxTokens           *int           `json:"max_tokens,omitempty"`
		MaxCompletionTokens *int           `json:"max_completion_tokens,omitempty"`
		MaxOutputTokens     *int           `json:"max_output_tokens,omitempty"`
		Temperature         *float64       `json:"temperature,omitempty"`
		TopP                *float64       `json:"top_p,omitempty"`
		ReasoningEffort     string         `json:"reasoning_effort,omitempty"`
		Reasoning           any            `json:"reasoning,omitempty"`
		Tools               jsontext.Value `json:"tools,omitempty"`
		ResponseFormat      jsontext.Value `json:"response_format,omitempty"`
	}
	if err := json.Unmarshal(body, &oreq); err != nil {
		return nil, "", fmt.Errorf("parse request: %w", err)
	}

	cleanModel := cleanResponsesModel(oreq.Model)

	var messages []struct {
		Role       string         `json:"role"`
		Content    jsontext.Value `json:"content"`
		ToolCallID string         `json:"tool_call_id,omitempty"`
		ToolCalls  []struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls,omitempty"`
	}
	if err := json.Unmarshal(oreq.Messages, &messages); err != nil {
		log.Warn("executor", "unmarshal messages", "error", err)
	}

	var inputItems []map[string]any
	instructions := oreq.Instructions
	var pendingToolCallIDs []string
	toolSeq := 0

	for i, msg := range messages {
		switch msg.Role {
		case "system", "developer":
			if instructions == "" {
				instructions = ExtractSimpleText(msg.Content)
			}
		case "user":
			inputItems = append(inputItems, convertUserContent(msg.Content))
		case "assistant":
			var textContent string
			if err := json.Unmarshal(msg.Content, &textContent); err == nil && textContent != "" {
				inputItems = append(inputItems, map[string]any{
					"type": "message",
					"role": "assistant",
					"content": []map[string]any{{
						"type": "output_text",
						"text": textContent,
					}},
				})
			} else {
				var blocks []map[string]any
				if err := json.Unmarshal(msg.Content, &blocks); err == nil && len(blocks) > 0 {
					var contentList []map[string]any
					for _, b := range blocks {
						if t, ok := b["text"].(string); ok && t != "" {
							contentList = append(contentList, map[string]any{
								"type": "output_text",
								"text": t,
							})
						}
					}
					if len(contentList) > 0 {
						inputItems = append(inputItems, map[string]any{
							"type":    "message",
							"role":    "assistant",
							"content": contentList,
						})
					}
				}
			}

			// Add assistant tool calls
			for tcIdx, tc := range msg.ToolCalls {
				name := strings.TrimSpace(tc.Function.Name)
				if name == "" {
					continue // Skip nameless calls — strict Responses upstreams reject them (#444)
				}
				cid := tc.ID
				if cid == "" {
					cid = fmt.Sprintf("call_%d_%d", toolSeq, tcIdx)
				}
				pendingToolCallIDs = append(pendingToolCallIDs, cid)
				inputItems = append(inputItems, map[string]any{
					"type":      "function_call",
					"call_id":   clampCallID(cid),
					"name":      name,
					"arguments": tc.Function.Arguments,
				})
			}
			toolSeq++
		case "tool":
			text := ExtractSimpleText(msg.Content)
			cid := msg.ToolCallID
			if cid != "" {
				for idx, p := range pendingToolCallIDs {
					if p == cid {
						pendingToolCallIDs = slices.Delete(pendingToolCallIDs, idx, idx+1)
						break
					}
				}
			} else if len(pendingToolCallIDs) > 0 {
				cid = pendingToolCallIDs[0]
				pendingToolCallIDs = pendingToolCallIDs[1:]
			} else {
				cid = fmt.Sprintf("call_tool_%d", i)
			}
			inputItems = append(inputItems, map[string]any{
				"type":    "function_call_output",
				"call_id": clampCallID(cid),
				"output":  text,
			})
		}
	}

	respReq := map[string]any{
		"model":  cleanModel,
		"input":  inputItems,
		"stream": true,
		"store":  false,
	}

	if instructions != "" {
		respReq["instructions"] = instructions
	}

	if oreq.MaxOutputTokens != nil {
		respReq["max_output_tokens"] = *oreq.MaxOutputTokens
	} else if oreq.MaxCompletionTokens != nil {
		respReq["max_output_tokens"] = *oreq.MaxCompletionTokens
	} else if oreq.MaxTokens != nil {
		respReq["max_output_tokens"] = *oreq.MaxTokens
	}
	if mot, ok := respReq["max_output_tokens"].(int); ok && mot < 16 && mot > 0 {
		respReq["max_output_tokens"] = 16
	}

	if oreq.Temperature != nil {
		respReq["temperature"] = *oreq.Temperature
	}
	if oreq.TopP != nil {
		respReq["top_p"] = *oreq.TopP
	}

	if oreq.ReasoningEffort != "" {
		rEffort := oreq.ReasoningEffort
		if rEffort == "max" {
			rEffort = "xhigh"
		}
		respReq["reasoning"] = map[string]any{
			"effort":  rEffort,
			"summary": "auto",
		}
	} else if oreq.Reasoning != nil {
		respReq["reasoning"] = oreq.Reasoning
	}

	// Chat Completions' response_format is the Responses API's text.format.
	// The Codex allowlist strips response_format, so without this mapping a
	// client asking for structured output silently got free text (#2896).
	if text, ok := responsesTextFormat(oreq.ResponseFormat); ok {
		respReq["text"] = map[string]any{"format": text}
	}

	// Tools
	if len(oreq.Tools) > 0 {
		var tools []struct {
			Type       string         `json:"type"`
			Function   jsontext.Value `json:"function,omitempty"`
			Name       string         `json:"name,omitempty"`
			Parameters map[string]any `json:"parameters,omitempty"`
			Strict     *bool          `json:"strict,omitempty"`
		}
		if err := json.Unmarshal(oreq.Tools, &tools); err == nil {
			var apiTools []map[string]any
			for _, t := range tools {
				tool := map[string]any{
					"type": "function",
					"name": t.Name,
				}
				// A flat-shaped tool carries its own parameters; without this
				// branch the Responses body dropped the schema entirely.
				if t.Parameters != nil {
					tool["parameters"] = StripCodexUnsupportedPatterns(t.Parameters)
				}
				strict := t.Strict
				if t.Function != nil {
					var fn struct {
						Name        string         `json:"name"`
						Description string         `json:"description"`
						Parameters  map[string]any `json:"parameters"`
						Strict      *bool          `json:"strict"`
					}
					if err := json.Unmarshal(t.Function, &fn); err == nil {
						tool["name"] = fn.Name
						if fn.Description != "" {
							tool["description"] = fn.Description
						}
						if fn.Parameters == nil {
							fn.Parameters = map[string]any{"type": "object", "properties": map[string]any{}}
						} else {
							if fn.Parameters["type"] == nil {
								fn.Parameters["type"] = "object"
							}
							if fn.Parameters["properties"] == nil {
								fn.Parameters["properties"] = map[string]any{}
							}
						}
						tool["parameters"] = StripCodexUnsupportedPatterns(fn.Parameters)
						// Nested strict (Chat Completions) wins over a flat one
						// (parity with decolua/9router#4543/#4573).
						if fn.Strict != nil {
							strict = fn.Strict
						}
					}
				}
				if strict != nil {
					tool["strict"] = *strict
				}
				apiTools = append(apiTools, tool)
			}
			if len(apiTools) > 0 {
				respReq["tools"] = apiTools
			}
		}
	}

	applyCodexModelShape(respReq, cleanModel)
	reqBody, err := json.Marshal(respReq)
	return reqBody, cleanModel, err
}

// responsesTextFormat maps a Chat Completions response_format onto the
// Responses API's text.format object (#2896). It reports false when the
// request carries nothing mappable: no response_format at all, an unknown
// type, or a json_schema with no schema (the Responses API has no way to
// express strict output without one), in which case no "text" field is added.
func responsesTextFormat(raw jsontext.Value) (map[string]any, bool) {
	if isAbsentJSON(raw) {
		return nil, false
	}
	var rf struct {
		Type       string         `json:"type"`
		JSONSchema jsontext.Value `json:"json_schema"`
	}
	if err := json.Unmarshal(raw, &rf); err != nil {
		return nil, false
	}

	switch rf.Type {
	case "json_object":
		return map[string]any{"type": "json_object"}, true
	case "json_schema":
	default:
		return nil, false
	}

	if isAbsentJSON(rf.JSONSchema) {
		return nil, false
	}
	var schema struct {
		Name   string         `json:"name"`
		Strict *bool          `json:"strict"`
		Schema jsontext.Value `json:"schema"`
	}
	if err := json.Unmarshal(rf.JSONSchema, &schema); err != nil {
		return nil, false
	}
	if isAbsentJSON(schema.Schema) {
		return nil, false
	}

	name := schema.Name
	if name == "" {
		name = "response"
	}
	// Chat Completions treats a missing strict flag as strict on Codex, so
	// only an explicit false turns it off.
	strict := schema.Strict == nil || *schema.Strict

	format := map[string]any{
		"type":   "json_schema",
		"name":   name,
		"strict": strict,
		"schema": schema.Schema,
	}
	return format, true
}

// isAbsentJSON reports whether raw holds nothing usable: no bytes at all, or
// an explicit JSON null.
func isAbsentJSON(raw jsontext.Value) bool {
	k := raw.Kind()
	return k == jsontext.KindInvalid || k == jsontext.KindNull
}

var unicodePropertyEscapeRegex = regexp.MustCompile(`(^|[^\\])(\\\\)*\\[pP]\{`)

// HasUnicodePropertyEscape reports whether a regex pattern string uses Unicode property escapes like \p{...}.
func HasUnicodePropertyEscape(pattern string) bool {
	return unicodePropertyEscapeRegex.MatchString(pattern)
}

// StripCodexUnsupportedPatterns strips \p{...} / \P{...} patterns from JSON Schema parameters (parity with #3922).
func StripCodexUnsupportedPatterns(schema map[string]any) map[string]any {
	cleaned := stripCodexNode(schema)
	if m, ok := cleaned.(map[string]any); ok {
		return m
	}
	return schema
}

func stripCodexNode(node any) any {
	switch v := node.(type) {
	case map[string]any:
		next := make(map[string]any, len(v))
		for k, val := range v {
			if k == "pattern" {
				if str, ok := val.(string); ok && HasUnicodePropertyEscape(str) {
					continue
				}
			}
			if k == "properties" {
				if props, ok := val.(map[string]any); ok {
					cleanedProps := make(map[string]any, len(props))
					for propName, propSchema := range props {
						cleanedProps[propName] = stripCodexNode(propSchema)
					}
					next[k] = cleanedProps
					continue
				}
			}
			next[k] = stripCodexNode(val)
		}
		return next
	case []any:
		next := make([]any, len(v))
		for i, item := range v {
			next[i] = stripCodexNode(item)
		}
		return next
	default:
		return v
	}
}

// Kimchi body cleaning helpers

var kimchiTopLevelDrops = []string{
	"anthropic_version",
	"anthropic_beta",
	"client_metadata",
	"mcp_servers",
	"stop_sequences",
	"thinking",
	"top_k",
}

// CleanKimchiBody strips Anthropic-specific fields from an OpenAI request body.
func CleanKimchiBody(body map[string]any) {
	if body == nil {
		return
	}

	mergeKimchiSystem(body)

	for _, key := range kimchiTopLevelDrops {
		delete(body, key)
	}
	delete(body, "system")

	stripKimchiMessageArtifacts(body)
	stripKimchiToolArtifacts(body)
	stripKimchiReasoningContent(body)
}

func mergeKimchiSystem(body map[string]any) {
	system, hasSystem := body["system"]
	if !hasSystem {
		return
	}

	systemText := KimchiSystemToText(system)
	if systemText == "" {
		return
	}

	msgs, ok := body["messages"].([]any)
	if !ok {
		return
	}

	for _, msg := range msgs {
		if m, ok := msg.(map[string]any); ok {
			if role, _ := m["role"].(string); role == "system" {
				switch c := m["content"].(type) {
				case string:
					m["content"] = systemText + "\n\n" + c
				case []any:
					m["content"] = append([]any{map[string]any{"type": "text", "text": systemText}}, c...)
				}
				return
			}
		}
	}

	body["messages"] = append([]any{map[string]any{"role": "system", "content": systemText}}, msgs...)
}

func KimchiSystemToText(system any) string {
	switch v := system.(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		var parts []string
		for _, part := range v {
			switch p := part.(type) {
			case string:
				parts = append(parts, p)
			case map[string]any:
				if t, ok := p["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	}
	return ""
}

func stripKimchiMessageArtifacts(body map[string]any) {
	msgs, ok := body["messages"].([]any)
	if !ok {
		return
	}

	for _, msg := range msgs {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		delete(m, "cache_control")

		content, ok := m["content"].([]any)
		if !ok {
			continue
		}

		for i, part := range content {
			p, ok := part.(map[string]any)
			if !ok {
				continue
			}
			delete(p, "cache_control")
			delete(p, "signature")
			content[i] = p
		}
	}
}

func stripKimchiToolArtifacts(body map[string]any) {
	tools, ok := body["tools"].([]any)
	if !ok {
		return
	}

	for i, tool := range tools {
		t, ok := tool.(map[string]any)
		if !ok {
			continue
		}
		delete(t, "cache_control")
		tools[i] = t
	}
}

func stripKimchiReasoningContent(body map[string]any) {
	msgs, ok := body["messages"].([]any)
	if !ok {
		return
	}

	const placeholderMaxLen = 8
	for _, msg := range msgs {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := m["role"].(string); role == "assistant" {
			if rc, ok := m["reasoning_content"].(string); ok && len(rc) > placeholderMaxLen {
				delete(m, "reasoning_content")
			}
		}
	}
}

// InjectReasoningContent ensures assistant messages in request body have reasoning_content
// injected (e.g. " " placeholder) for providers like opencode, deepseek, or kimi models.
func InjectReasoningContent(body []byte, provider string) []byte {
	var reqMap map[string]any
	if err := json.Unmarshal(body, &reqMap); err != nil {
		return body
	}

	msgs, ok := reqMap["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return body
	}

	modelStr, _ := reqMap["model"].(string)
	modelLower := strings.ToLower(modelStr)

	isDeepSeek := strings.Contains(modelLower, "deepseek") || provider == "opencode" || provider == "opencode-go"
	isKimi := strings.HasPrefix(modelLower, "kimi-")

	if !isDeepSeek && !isKimi {
		return body
	}

	changed := false
	for i, m := range msgs {
		msgMap, ok := m.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msgMap["role"].(string)
		if role != "assistant" {
			continue
		}

		rc, hasRC := msgMap["reasoning_content"].(string)
		if hasRC && len(rc) > 0 {
			continue
		}

		if isKimi {
			toolCalls, hasTC := msgMap["tool_calls"].([]any)
			if !hasTC || len(toolCalls) == 0 {
				continue
			}
		}

		msgMap["reasoning_content"] = " "
		msgs[i] = msgMap
		changed = true
	}

	if !changed {
		return body
	}

	reqMap["messages"] = msgs
	// Deterministic marshaling keeps the serialized prefix stable across the
	// turns of a session, which DeepSeek's prefix prompt cache requires.
	newBody, err := marshalStable(reqMap)
	if err != nil {
		return body
	}
	return newBody
}
