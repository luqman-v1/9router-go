package executor

import (
	"9router/proxy/internal/log"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"strings"
)

// ExtractSimpleText extracts text from jsontext.Value (string or array[text]).
func ExtractSimpleText(raw jsontext.Value) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []map[string]interface{}
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
func convertUserContent(raw jsontext.Value) map[string]interface{} {
	result := map[string]interface{}{
		"type": "message",
		"role": "user",
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil && text != "" {
		result["content"] = []map[string]interface{}{
			{"type": "input_text", "text": text},
		}
		return result
	}

	var blocks []map[string]interface{}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var content []map[string]interface{}
		for _, b := range blocks {
			if t, ok := b["text"].(string); ok && t != "" {
				content = append(content, map[string]interface{}{
					"type": "input_text",
					"text": t,
				})
			}
			if img, ok := b["image_url"].(map[string]interface{}); ok {
				if url, ok := img["url"].(string); ok {
					content = append(content, map[string]interface{}{
						"type":      "input_image",
						"image_url": url,
					})
				}
			}
		}
		if len(content) > 0 {
			result["content"] = content
		} else {
			result["content"] = []map[string]interface{}{
				{"type": "input_text", "text": "..."},
			}
		}
		return result
	}

	result["content"] = []map[string]interface{}{
		{"type": "input_text", "text": "..."},
	}
	return result
}

func cleanResponsesModel(model string) string {
	clean := strings.TrimPrefix(model, "oc/")
	clean = strings.TrimPrefix(clean, "opencode/")
	if idx := strings.IndexByte(clean, '('); idx != -1 {
		clean = clean[:idx]
	}
	return clean
}

func clampCallID(id string) string {
	if len(id) > 64 {
		return id[:64]
	}
	return id
}

// buildResponsesBody transforms OpenAI Chat Completions body → Responses API body.
// Returns (responsesBody, modelName, error).
func buildResponsesBody(body []byte) ([]byte, string, error) {
	// If the body is already in Responses API format (has input[]), normalize fields and return
	var quickCheck struct {
		Input               []any  `json:"input"`
		Model               string `json:"model"`
		MaxTokens           *int   `json:"max_tokens,omitempty"`
		MaxCompletionTokens *int   `json:"max_completion_tokens,omitempty"`
		MaxOutputTokens     *int   `json:"max_output_tokens,omitempty"`
	}
	if err := json.Unmarshal(body, &quickCheck); err == nil && len(quickCheck.Input) > 0 {
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
			delete(m, "max_tokens")
			delete(m, "max_completion_tokens")

			// Clamp call_id in existing input items
			if inList, ok := m["input"].([]any); ok {
				for _, item := range inList {
					if itemMap, ok := item.(map[string]any); ok {
						if cid, ok := itemMap["call_id"].(string); ok && len(cid) > 64 {
							itemMap["call_id"] = cid[:64]
						}
					}
				}
			}

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

	var inputItems []map[string]interface{}
	instructions := oreq.Instructions

	for _, msg := range messages {
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
				inputItems = append(inputItems, map[string]interface{}{
					"type": "message",
					"role": "assistant",
					"content": []map[string]interface{}{{
						"type": "output_text",
						"text": textContent,
					}},
				})
			} else {
				var blocks []map[string]interface{}
				if err := json.Unmarshal(msg.Content, &blocks); err == nil && len(blocks) > 0 {
					var contentList []map[string]interface{}
					for _, b := range blocks {
						if t, ok := b["text"].(string); ok && t != "" {
							contentList = append(contentList, map[string]interface{}{
								"type": "output_text",
								"text": t,
							})
						}
					}
					if len(contentList) > 0 {
						inputItems = append(inputItems, map[string]interface{}{
							"type":    "message",
							"role":    "assistant",
							"content": contentList,
						})
					}
				}
			}

			// Add assistant tool calls
			for _, tc := range msg.ToolCalls {
				inputItems = append(inputItems, map[string]interface{}{
					"type":      "function_call",
					"call_id":   clampCallID(tc.ID),
					"name":      tc.Function.Name,
					"arguments": tc.Function.Arguments,
				})
			}
		case "tool":
			text := ExtractSimpleText(msg.Content)
			inputItems = append(inputItems, map[string]interface{}{
				"type":    "function_call_output",
				"call_id": clampCallID(msg.ToolCallID),
				"output":  text,
			})
		}
	}

	respReq := map[string]interface{}{
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
		respReq["reasoning"] = map[string]interface{}{
			"effort":  rEffort,
			"summary": "auto",
		}
	} else if oreq.Reasoning != nil {
		respReq["reasoning"] = oreq.Reasoning
	}

	// Tools
	if len(oreq.Tools) > 0 {
		var tools []struct {
			Type     string         `json:"type"`
			Function jsontext.Value `json:"function,omitempty"`
			Name     string         `json:"name,omitempty"`
		}
		if err := json.Unmarshal(oreq.Tools, &tools); err == nil {
			var apiTools []map[string]interface{}
			for _, t := range tools {
				tool := map[string]interface{}{
					"type": "function",
					"name": t.Name,
				}
				if t.Function != nil {
					var fn struct {
						Name        string         `json:"name"`
						Description string         `json:"description"`
						Parameters  map[string]any `json:"parameters"`
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
						tool["parameters"] = fn.Parameters
					}
				}
				apiTools = append(apiTools, tool)
			}
			if len(apiTools) > 0 {
				respReq["tools"] = apiTools
			}
		}
	}

	reqBody, err := json.Marshal(respReq)
	return reqBody, cleanModel, err
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
	newBody, err := json.Marshal(reqMap)
	if err != nil {
		return body
	}
	return newBody
}
