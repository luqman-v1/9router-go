package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/constants"
)

// bypassResponse represents a fake response for bypassed requests.
type bypassResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int `json:"index"`
		Message      *struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message,omitempty"`
		Delta *struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"delta,omitempty"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage,omitempty"`
}

const defaultBypassText = "CLI Command Execution: Clear Terminal"
const namingBypassText = "{\"isNewTopic\":true,\"title\":\"Project Setup\"}"

// handleBypassRequest checks if a request is a synthetic bypass request
// (Claude Code naming, warmup, keepalive) and writes a fake response if so.
// Returns true if the request was handled (bypass activated).
// Matches Next.js handleBypassRequest in open-sse/utils/bypassHandler.js.
func handleBypassRequest(w http.ResponseWriter, body []byte, model string, isStream bool) bool {
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
		System any `json:"system"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return false
	}
	if len(req.Messages) == 0 {
		return false
	}

	shouldBypass := false
	isNaming := false

	// Helper to extract text from content (string or array of blocks)
	getText := func(content any) string {
		if content == nil {
			return ""
		}
		if s, ok := content.(string); ok {
			return s
		}
		if arr, ok := content.([]any); ok {
			var texts []string
			for _, item := range arr {
				if m, ok := item.(map[string]any); ok {
					if t, ok := m["text"].(string); ok {
						texts = append(texts, t)
					}
				}
			}
			return strings.Join(texts, " ")
		}
		return ""
	}

	last := req.Messages[len(req.Messages)-1]

	// Pattern 1: Naming (assistant message = "{")
	if last.Role == "assistant" {
		if text := getText(last.Content); strings.TrimSpace(text) == "{" {
			shouldBypass = true
		}
	}

	// Pattern 2: Warmup
	if !shouldBypass {
		firstText := getText(req.Messages[0].Content)
		if firstText == "Warmup" {
			shouldBypass = true
		}
	}

	// Pattern 3: Count
	if !shouldBypass && len(req.Messages) == 1 && req.Messages[0].Role == "user" {
		if getText(req.Messages[0].Content) == "count" {
			shouldBypass = true
		}
	}

	// Pattern 4: Claude Code naming (isNewTopic)
	if !shouldBypass {
		// Check system field from body (Claude format sends system at top level)
		systemText := ""
		if sysStr, ok := req.System.(string); ok {
			systemText = sysStr
		} else if sysArr, ok := req.System.([]any); ok {
			for _, item := range sysArr {
				if m, ok := item.(map[string]any); ok {
					if t, ok := m["text"].(string); ok {
						systemText += t + " "
					}
				}
			}
		}
		// Also check system message in messages array
		for _, msg := range req.Messages {
			if msg.Role == "system" {
				systemText += getText(msg.Content) + " "
			}
		}
		if strings.Contains(systemText, "isNewTopic") {
			shouldBypass = true
			isNaming = true
		}
	}

	if !shouldBypass {
		return false
	}

	now := time.Now()
	created := now.Unix()
	id := "chatcmpl-" + now.Format("150405000000")

	respText := defaultBypassText
	if isNaming {
		respText = namingBypassText
	}

	if isStream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		// First chunk: role + content
		firstChunk := map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": []map[string]any{
				{
					"index": 0,
					"delta": map[string]any{
						"role":    "assistant",
						"content": respText,
					},
					"finish_reason": nil,
				},
			},
		}
		fc, _ := json.Marshal(firstChunk)
		w.Write([]byte("data: "))
		w.Write(fc)
		w.Write([]byte("\n\n"))
		if flusher != nil {
			flusher.Flush()
		}

		// Final chunk: finish_reason + usage
		finalChunk := map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": []map[string]any{
				{
					"index": 0,
					"delta":       map[string]any{},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]any{
				"prompt_tokens":     1,
				"completion_tokens": 1,
				"total_tokens":      2,
			},
		}
		final, _ := json.Marshal(finalChunk)
		w.Write([]byte("data: "))
		w.Write(final)
		w.Write([]byte("\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	} else {
		resp := map[string]any{
			"id":      id,
			"object":  "chat.completion",
			"created": created,
			"model":   model,
			"choices": []map[string]any{
				{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": respText,
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]any{
				"prompt_tokens":     1,
				"completion_tokens": 1,
				"total_tokens":      2,
			},
		}
		respBytes, _ := json.Marshal(resp)
		w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
		w.WriteHeader(http.StatusOK)
		w.Write(respBytes)
	}
	return true
}
