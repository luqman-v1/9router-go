package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/translator"
)

// forwardKiroRequest handles forwarding for Kiro/AWS CodeWhisperer with EventStream binary response.
func (h *ChatHandler) forwardKiroRequest(
	w http.ResponseWriter,
	cfg *providers.ProviderConfig,
	apiKey string,
	body []byte,
	isStream bool,
	translateResponse bool,
	metrics *streamMetrics,
) error {
	// OAuth token refresh
	// Kiro OAuth refresh is handled by the generic method in fallback.go

	// 1. Extract model from body
	var oreq struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &oreq)
	modelName := oreq.Model
	if modelName == "" {
		modelName = "grok-4"
	}

	// 2. Build headers
	invocationID := fmt.Sprintf("%d-%d", time.Now().UnixMilli(), time.Now().UnixNano())
	req, err := http.NewRequest("POST", cfg.BaseURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.amazon.eventstream")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Amz-Target", "AmazonCodeWhispererStreamingService.GenerateAssistantResponse")
	req.Header.Set("Amz-Sdk-Request", "attempt=1; max=3")
	req.Header.Set("Amz-Sdk-Invocation-Id", invocationID)

	// 3. Send request
	resp, err := h.Client.Do(req)
	if err != nil {
		return fmt.Errorf("upstream request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return &upstreamError{StatusCode: resp.StatusCode, Body: errBody}
	}

	// 4. Handle response (streaming only — Kiro is always streaming from EventStream)
	return h.handleKiroStream(w, resp.Body, translateResponse, metrics)
}

// handleKiroStream reads AWS EventStream binary and translates to OpenAI SSE.
func (h *ChatHandler) handleKiroStream(w http.ResponseWriter, upstream io.Reader, translateResponse bool, metrics *streamMetrics) error {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	esr := providers.NewEventStreamReader(upstream)

	responseID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	created := time.Now().Unix()
	state := &kiroStreamState{}

	var accumulatedContent strings.Builder

	for {
		frame, err := esr.ReadFrame()
		if err != nil {
			log.Printf("[kiro] eventstream error: %v", err)
			break
		}
		if frame == nil {
			break // stream ended
		}

		eventType := frame.Headers[":event-type"]

		switch eventType {
		case "assistantResponseEvent":
			var payload struct {
				Content string `json:"content"`
			}
			if err := json.Unmarshal(frame.Payload, &payload); err != nil {
				continue
			}
			if payload.Content == "" {
				continue
			}
			accumulatedContent.WriteString(payload.Content)

			chunk := map[string]interface{}{
				"id":      responseID,
				"object":  "chat.completion.chunk",
				"created": created,
				"choices": []map[string]interface{}{{
					"index": 0,
					"delta": map[string]interface{}{"content": payload.Content},
				}},
			}
			writeSSE(w, chunk)
			if flusher != nil {
				flusher.Flush()
			}

		case "reasoningContentEvent":
			var payload struct {
				Text    string `json:"text"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal(frame.Payload, &payload); err != nil {
				continue
			}
			reasoning := payload.Text
			if reasoning == "" {
				reasoning = payload.Content
			}
			if reasoning == "" {
				continue
			}

			chunk := map[string]interface{}{
				"id":      responseID,
				"object":  "chat.completion.chunk",
				"created": created,
				"choices": []map[string]interface{}{{
					"index": 0,
					"delta": map[string]interface{}{"reasoning_content": reasoning},
				}},
			}
			writeSSE(w, chunk)
			if flusher != nil {
				flusher.Flush()
			}

		case "toolUseEvent":
			var toolUse struct {
				ToolUseID string          `json:"toolUseId"`
				Name      string          `json:"name"`
				Input     json.RawMessage `json:"input"`
			}
			if err := json.Unmarshal(frame.Payload, &toolUse); err != nil {
				continue
			}
			if toolUse.Name == "" {
				continue
			}

			state.toolCallIndex++
			args := string(toolUse.Input)
			if args == "" {
				args = "{}"
			}

			chunk := map[string]interface{}{
				"id":      responseID,
				"object":  "chat.completion.chunk",
				"created": created,
				"choices": []map[string]interface{}{{
					"index": 0,
					"delta": map[string]interface{}{
						"tool_calls": []map[string]interface{}{{
							"index":    state.toolCallIndex,
							"id":       toolUse.ToolUseID,
							"type":     "function",
							"function": map[string]interface{}{"name": toolUse.Name, "arguments": args},
						}},
					},
				}},
			}
			writeSSE(w, chunk)
			if flusher != nil {
				flusher.Flush()
			}

		case "codeEvent":
			var payload struct {
				Content string `json:"content"`
			}
			if err := json.Unmarshal(frame.Payload, &payload); err != nil {
				continue
			}
			if payload.Content == "" {
				continue
			}
			chunk := map[string]interface{}{
				"id":      responseID,
				"object":  "chat.completion.chunk",
				"created": created,
				"choices": []map[string]interface{}{{
					"index": 0,
					"delta": map[string]interface{}{"content": payload.Content},
				}},
			}
			writeSSE(w, chunk)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}

	// Emit finish + stop
	finishReason := "stop"
	if state.toolCallIndex > 0 {
		finishReason = "tool_calls"
	}
	done := map[string]interface{}{
		"id":      responseID,
		"object":  "chat.completion.chunk",
		"created": created,
		"choices": []map[string]interface{}{{
			"index":        0,
			"delta":        map[string]interface{}{},
			"finish_reason": finishReason,
		}},
	}
	writeSSE(w, done)
	w.Write([]byte("data: [DONE]\n\n"))
	if flusher != nil {
		flusher.Flush()
	}

	// Set usage from accumulated content (estimation)
	inputTokens := 0
	outputTokens := len(accumulatedContent.String()) / 4
	translator.SetLastUsage(&translator.OpenAIUsage{
		PromptTokens:     inputTokens,
		CompletionTokens: outputTokens,
	})

	return nil
}

// kiroStreamState tracks tool call indices during Kiro stream processing.
type kiroStreamState struct {
	toolCallIndex int
}

// writeSSE writes an SSE-formatted JSON event to the response writer.
func writeSSE(w io.Writer, data interface{}) {
	b, _ := json.Marshal(data)
	w.Write([]byte(fmt.Sprintf("data: %s\n\n", string(b))))
}
