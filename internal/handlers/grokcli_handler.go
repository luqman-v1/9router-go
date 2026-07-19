package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"9router/proxy/internal/providers"
)

// forwardGrokCLIRequest forwards a Chat Completions request to the Grok CLI Responses API.
// Same responses SSE format as Codex (response.output_text.delta, response.completed, etc.)
// but with Grok-specific headers and base URL.
func (h *ChatHandler) forwardGrokCLIRequest(
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
		Model     string          `json:"model"`
		Messages  json.RawMessage `json:"messages"`
		Stream    bool            `json:"stream,omitempty"`
		MaxTokens int             `json:"max_tokens,omitempty"`
		Tools     json.RawMessage `json:"tools,omitempty"`
	}
	if err := json.Unmarshal(body, &oreq); err != nil {
		return fmt.Errorf("parse request: %w", err)
	}

	// 2. Build Responses API input from messages (same as codex)
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
				"type":    "function_call_output",
				"call_id": toolCallID,
				"output":  text,
			})
		}
	}

	// 3. Build Responses API request
	respReq := map[string]interface{}{
		"model":  oreq.Model,
		"input":  inputItems,
		"stream": true,
		"store":  false,
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
			var grokTools []map[string]interface{}
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
				grokTools = append(grokTools, tool)
			}
			if len(grokTools) > 0 {
				respReq["tools"] = grokTools
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
	req.Header.Set("x-grok-client-identifier", "grok-cli-go")
	req.Header.Set("x-grok-client-version", "0.1.0")

	resp, err := h.Client.Do(req)
	if err != nil {
		return fmt.Errorf("upstream request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return &upstreamError{StatusCode: resp.StatusCode, Body: errBody}
	}

	// 6. Handle stream response (same SSE format as codex)
	return h.handleCodexStream(w, resp.Body)
}
