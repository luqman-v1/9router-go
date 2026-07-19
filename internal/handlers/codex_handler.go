package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/translator"
)

// forwardCodexRequest handles forwarding for Codex/OpenAI Responses API.
func (h *ChatHandler) forwardCodexRequest(
	w http.ResponseWriter,
	cfg *providers.ProviderConfig,
	apiKey string,
	body []byte,
	isStream bool,
	translateResponse bool,
	metrics *streamMetrics,
) error {
	// 1. Parse incoming OpenAI-format body
	var oreq struct {
		Model        string          `json:"model"`
		Messages     json.RawMessage `json:"messages"`
		Instructions string          `json:"instructions,omitempty"`
		MaxTokens    int             `json:"max_tokens,omitempty"`
		Stream       bool            `json:"stream,omitempty"`
		Tools        json.RawMessage `json:"tools,omitempty"`
	}
	if err := json.Unmarshal(body, &oreq); err != nil {
		return fmt.Errorf("parse request: %w", err)
	}

	// 2. Build Responses API input from messages
	var messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	_ = json.Unmarshal(oreq.Messages, &messages)

	var inputItems []map[string]interface{}

	for _, msg := range messages {
		switch msg.Role {
		case "system":
			text := extractSimpleText(msg.Content)
			if text != "" {
				inputItems = append(inputItems, map[string]interface{}{
					"type": "message",
					"role": "developer",
					"content": []map[string]interface{}{{
						"type": "input_text",
						"text": text,
					}},
				})
			}
		case "user":
			inputItems = append(inputItems, convertUserContent(msg.Content))
		case "assistant":
			// Parse content + tool_calls
			aiItem := map[string]interface{}{
				"type": "message",
				"role": "assistant",
			}
			var textContent string
			if err := json.Unmarshal(msg.Content, &textContent); err == nil {
				if textContent != "" {
					aiItem["content"] = []map[string]interface{}{{
						"type": "input_text",
						"text": textContent,
					}}
				}
			} else {
				var blocks []map[string]interface{}
				if err := json.Unmarshal(msg.Content, &blocks); err == nil {
					for _, b := range blocks {
						if t, ok := b["text"].(string); ok && t != "" {
							aiItem["content"] = []map[string]interface{}{{
								"type": "input_text",
								"text": t,
							}}
							break
						}
					}
				}
			}
			inputItems = append(inputItems, aiItem)
		case "tool":
			var toolCallID string
			_ = json.Unmarshal(msg.Content, &toolCallID)
			text := extractSimpleText(msg.Content)
			inputItems = append(inputItems, map[string]interface{}{
				"type":      "function_call_output",
				"call_id":   toolCallID,
				"output":    text,
			})
		}
	}

	// 3. Build Responses API request
	respReq := map[string]interface{}{
		"model":    oreq.Model,
		"input":    inputItems,
		"stream":   true,
		"store":    false,
	}

	if oreq.Instructions != "" {
		respReq["instructions"] = oreq.Instructions
	}
	if oreq.MaxTokens > 0 {
		respReq["max_output_tokens"] = oreq.MaxTokens
	}

	// 4. Tools (simplified: function tools only)
	if len(oreq.Tools) > 0 {
		var tools []struct {
			Type     string          `json:"type"`
			Function json.RawMessage `json:"function,omitempty"`
			Name     string          `json:"name,omitempty"`
		}
		if err := json.Unmarshal(oreq.Tools, &tools); err == nil {
			var codexTools []map[string]interface{}
			for _, t := range tools {
				tool := map[string]interface{}{
					"type": "function",
					"name": t.Name,
				}
				if t.Function != nil {
					var fn struct {
						Name        string          `json:"name"`
						Description string          `json:"description"`
						Parameters  json.RawMessage `json:"parameters"`
					}
					json.Unmarshal(t.Function, &fn)
					tool["name"] = fn.Name
					if fn.Description != "" {
						tool["description"] = fn.Description
					}
					if fn.Parameters != nil {
						tool["parameters"] = fn.Parameters
					}
				}
				codexTools = append(codexTools, tool)
			}
			if len(codexTools) > 0 {
				respReq["tools"] = codexTools
			}
		}
	}

	// 5. Build HTTP request
	reqBody, _ := json.Marshal(respReq)
	baseURL := strings.TrimRight(cfg.BaseURL, "/")

	req, err := http.NewRequest("POST", baseURL, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("originator", "codex_cli_rs")

	resp, err := h.Client.Do(req)
	if err != nil {
		return fmt.Errorf("upstream request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return &upstreamError{StatusCode: resp.StatusCode, Body: errBody}
	}

	// 6. Handle stream response
	return h.handleCodexStream(w, resp.Body)
}

// handleCodexStream parses Codex Responses API SSE stream → OpenAI Chat SSE.
func (h *ChatHandler) handleCodexStream(w http.ResponseWriter, upstream io.Reader) error {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	responseID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	created := time.Now().Unix()
	codexStreamState := &codexStreamState{}

	buf := make([]byte, 64*1024)
	var leftover string

	for {
		n, err := upstream.Read(buf)
		if n > 0 {
			text := leftover + string(buf[:n])
			leftover = ""

			for _, line := range strings.Split(text, "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}

				// Handle SSE event type line
				if strings.HasPrefix(line, "event: ") {
					codexStreamState.currentEvent = line[7:]
					continue
				}

				// Handle SSE data line
				if strings.HasPrefix(line, "data: ") {
					data := line[6:]
					if data == "[DONE]" {
						continue
					}
					out := processCodexEvent(data, codexStreamState, responseID, created)
					for _, chunk := range out {
						w.Write([]byte(chunk))
					}
					if flusher != nil {
						flusher.Flush()
					}
				}
			}
		}
		if err != nil {
			break
		}
	}

	// Emit final DONE
	w.Write([]byte("data: [DONE]\n\n"))
	if flusher != nil {
		flusher.Flush()
	}

	// Set estimated usage
	translator.SetLastUsage(&translator.OpenAIUsage{
		CompletionTokens: codexStreamState.outputLength / 4,
	})

	return nil
}

type codexStreamState struct {
	currentEvent  string
	outputLength  int
	toolCallCount int
}

func processCodexEvent(data string, state *codexStreamState, responseID string, created int64) []string {
	var event map[string]interface{}
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return nil
	}

	eventType, _ := event["type"].(string)

	switch eventType {
	case "response.output_text.delta":
		delta, _ := event["delta"].(string)
		if delta == "" {
			return nil
		}
		state.outputLength += len(delta)
		chunk := map[string]interface{}{
			"id":      responseID,
			"object":  "chat.completion.chunk",
			"created": created,
			"choices": []map[string]interface{}{{
				"index": 0,
				"delta": map[string]interface{}{"content": delta},
			}},
		}
		b, _ := json.Marshal(chunk)
		return []string{fmt.Sprintf("data: %s\n\n", string(b))}

	case "response.function_call_arguments.delta":
		delta, _ := event["delta"].(string)
		if delta == "" {
			return nil
		}
		state.toolCallCount++
		chunk := map[string]interface{}{
			"id":      responseID,
			"object":  "chat.completion.chunk",
			"created": created,
			"choices": []map[string]interface{}{{
				"index": 0,
				"delta": map[string]interface{}{
					"tool_calls": []map[string]interface{}{{
						"index":    state.toolCallCount,
						"id":       fmt.Sprintf("call_%d", state.toolCallCount),
						"type":     "function",
						"function": map[string]interface{}{"arguments": delta},
					}},
				},
			}},
		}
		b, _ := json.Marshal(chunk)
		return []string{fmt.Sprintf("data: %s\n\n", string(b))}

	case "response.function_call_arguments.done":
		name, _ := event["name"].(string)
		args, _ := event["arguments"].(string)
		if name == "" {
			return nil
		}
		chunk := map[string]interface{}{
			"id":      responseID,
			"object":  "chat.completion.chunk",
			"created": created,
			"choices": []map[string]interface{}{{
				"index": 0,
				"delta": map[string]interface{}{
					"tool_calls": []map[string]interface{}{{
						"function": map[string]interface{}{
							"name":      name,
							"arguments": args,
						},
					}},
				},
			}},
		}
		b, _ := json.Marshal(chunk)
		return []string{fmt.Sprintf("data: %s\n\n", string(b))}

	case "response.completed":
		// Emit finish_reason
		chunk := map[string]interface{}{
			"id":      responseID,
			"object":  "chat.completion.chunk",
			"created": created,
			"choices": []map[string]interface{}{{
				"index":        0,
				"delta":        map[string]interface{}{},
				"finish_reason": "stop",
			}},
		}
		b, _ := json.Marshal(chunk)
		return []string{fmt.Sprintf("data: %s\n\n", string(b))}
	}

	return nil
}

// extractSimpleText extracts a simple text string from JSON RawMessage (string or array with text block).
func extractSimpleText(raw json.RawMessage) string {
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
func convertUserContent(raw json.RawMessage) map[string]interface{} {
	result := map[string]interface{}{
		"type": "message",
		"role": "user",
	}

	// Try string first
	var text string
	if err := json.Unmarshal(raw, &text); err == nil && text != "" {
		result["content"] = []map[string]interface{}{
			{"type": "input_text", "text": text},
		}
		return result
	}

	// Try array of content blocks
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
						"type":       "input_image",
						"image_url":  url,
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
