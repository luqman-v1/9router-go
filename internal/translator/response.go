package translator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func extractReasoningText(delta OpenAIDelta) string {
	if delta.ReasoningContent != "" {
		return delta.ReasoningContent
	}
	if delta.Reasoning != "" {
		return delta.Reasoning
	}
	return ""
}

func formatSSE(event map[string]any) string {
	eventType, _ := event["type"].(string)
	payload, _ := json.Marshal(event)
	return fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, string(payload))
}

func stopThinkingBlock(state *StreamState, results *[]map[string]any) {
	if !state.ThinkingBlockStarted {
		return
	}
	*results = append(*results, map[string]any{
		"type":  "content_block_stop",
		"index": state.ThinkingBlockIndex,
	})
	state.ThinkingBlockStarted = false
}

func stopTextBlock(state *StreamState, results *[]map[string]any) {
	if !state.TextBlockStarted || state.TextBlockClosed {
		return
	}
	state.TextBlockClosed = true
	*results = append(*results, map[string]any{
		"type":  "content_block_stop",
		"index": state.TextBlockIndex,
	})
	state.TextBlockStarted = false
}

// TranslateOpenAIToClaude converts a non-streaming OpenAI response to Claude message format.
func TranslateOpenAIToClaude(openaiResp []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(openaiResp)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("empty response body")
	}

	var resp OpenAIResponse
	if err := json.Unmarshal(trimmed, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse OpenAI response: %w", err)
	}

	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("no choices in OpenAI response")
	}

	choice := resp.Choices[0]
	msg := choice.Message

	msgID := resp.ID
	if msgID == "" {
		msgID = fmt.Sprintf("msg_%d", time.Now().UnixNano())
	} else {
		msgID = strings.Replace(msgID, "chatcmpl-", "", 1)
		if msgID == "" || msgID == "chat" {
			msgID = fmt.Sprintf("msg_%d", time.Now().UnixNano())
		}
	}
	modelName := resp.Model
	if modelName == "" {
		modelName = "claude-3-5-sonnet"
	}

	var contentBlocks []map[string]any

	// Reasoning
	reasoning := msg.ReasoningContent
	if reasoning == "" {
		reasoning = msg.Reasoning
	}
	if reasoning != "" {
		contentBlocks = append(contentBlocks, map[string]any{
			"type":     "thinking",
			"thinking": reasoning,
		})
	}

	// Text content
	if msg.Content != "" {
		contentBlocks = append(contentBlocks, map[string]any{
			"type": "text",
			"text": msg.Content,
		})
	}

	// Tool calls
	for _, tc := range msg.ToolCalls {
		toolName := tc.ID
		if tc.Function != nil && tc.Function.Name != "" {
			toolName = tc.Function.Name
		}
		toolName, _ = strings.CutPrefix(toolName, "proxy_")
		var input json.RawMessage
		if tc.Function != nil && tc.Function.Arguments != "" {
			sanitized := sanitizeToolArgs(toolName, tc.Function.Arguments)
			input = json.RawMessage(sanitized)
		} else {
			input = json.RawMessage("{}")
		}
		contentBlocks = append(contentBlocks, map[string]any{
			"type":  "tool_use",
			"id":    tc.ID,
			"name":  toolName,
			"input": input,
		})
	}

	if len(contentBlocks) == 0 {
		contentBlocks = []map[string]any{}
	}

	// Stop reason
	claudeStop := "end_turn"
	if choice.FinishReason != nil {
		switch *choice.FinishReason {
		case "stop":
			claudeStop = "end_turn"
		case "length":
			claudeStop = "max_tokens"
		case "tool_calls":
			claudeStop = "tool_use"
		}
	}

	// Usage
	inputTokens, outputTokens := 0, 0
	var details *CompletionTokensDetails
	if resp.Usage != nil {
		inputTokens = resp.Usage.PromptTokens
		outputTokens = resp.Usage.CompletionTokens
		details = resp.Usage.CompletionTokensDetails
	}
	SetLastUsage(&OpenAIUsage{
		PromptTokens:            inputTokens,
		CompletionTokens:        outputTokens,
		CompletionTokensDetails: details,
	})

	result := map[string]any{
		"id":            msgID,
		"type":          "message",
		"role":          "assistant",
		"model":         modelName,
		"content":       contentBlocks,
		"stop_reason":   claudeStop,
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
		},
	}

	return json.Marshal(result)
}

// TranslateOpenAIToClaudeStream converts a single OpenAI SSE chunk JSON payload to Claude SSE format.
func TranslateOpenAIToClaudeStream(openaiChunk []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(openaiChunk)
	if len(trimmed) == 0 {
		return nil, nil
	}

	var isDone bool
	var dataPart []byte
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		dataStr := string(bytes.TrimSpace(trimmed[5:]))
		if dataStr == "[DONE]" {
			isDone = true
		} else {
			dataPart = []byte(dataStr)
		}
	} else {
		dataPart = trimmed
	}

	if isDone {
		return []byte("data: [DONE]\n\n"), nil
	}

	var chunk OpenAIChunk
	if err := json.Unmarshal(dataPart, &chunk); err != nil {
		return nil, err
	}

	stateKey := chunk.ID
	if stateKey == "" {
		stateKey = "default-session"
	}

	statesMu.Lock()
	state, exists := states[stateKey]
	if !exists {
		cleanID := strings.Replace(chunk.ID, "chatcmpl-", "", 1)
		if cleanID == "" || cleanID == "chat" {
			cleanID = fmt.Sprintf("msg_%d", time.Now().UnixNano())
		}
		modelName := chunk.Model
		if modelName == "" {
			modelName = "claude-3-5-sonnet"
		}
		state = &StreamState{
			MessageId:      cleanID,
			Model:          modelName,
			ToolCalls:      make(map[int]ToolCallState),
			ToolArgBuffers: make(map[int]string),
		}
		states[stateKey] = state
	}
	statesMu.Unlock()

	if chunk.Usage != nil {
		if state.Usage == nil {
			state.Usage = &OpenAIUsage{}
		}
		if chunk.Usage.PromptTokens > 0 {
			state.Usage.PromptTokens = chunk.Usage.PromptTokens
		}
		if chunk.Usage.CompletionTokens > 0 {
			state.Usage.CompletionTokens = chunk.Usage.CompletionTokens
		}
		if chunk.Usage.CachedTokens > 0 {
			state.Usage.CachedTokens = chunk.Usage.CachedTokens
		}
		if chunk.Usage.CompletionTokensDetails != nil {
			state.Usage.CompletionTokensDetails = chunk.Usage.CompletionTokensDetails
		}
	}

	var results []map[string]any

	// 1. Message Start
	if !state.MessageStartSent {
		state.MessageStartSent = true
		results = append(results, map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id":            state.MessageId,
				"type":          "message",
				"role":          "assistant",
				"model":         state.Model,
				"content":       []any{},
				"stop_reason":   nil,
				"stop_sequence": nil,
				"usage": map[string]any{
					"input_tokens":  0,
					"output_tokens": 0,
				},
			},
		})
	}

	if len(chunk.Choices) == 0 {
		if len(results) > 0 {
			var buf bytes.Buffer
			for _, res := range results {
				buf.WriteString(formatSSE(res))
			}
			return buf.Bytes(), nil
		}
		return nil, nil
	}

	choice := chunk.Choices[0]
	delta := choice.Delta

	// 2. Reasoning
	reasoningContent := extractReasoningText(delta)
	if reasoningContent != "" {
		stopTextBlock(state, &results)
		if !state.ThinkingBlockStarted {
			state.ThinkingBlockIndex = state.NextBlockIndex
			state.NextBlockIndex++
			state.ThinkingBlockStarted = true
			results = append(results, map[string]any{
				"type":  "content_block_start",
				"index": state.ThinkingBlockIndex,
				"content_block": map[string]any{
					"type":     "thinking",
					"thinking": "",
				},
			})
		}
		results = append(results, map[string]any{
			"type":  "content_block_delta",
			"index": state.ThinkingBlockIndex,
			"delta": map[string]any{
				"type":     "thinking_delta",
				"thinking": reasoningContent,
			},
		})
	}

	// 3. Content
	if delta.Content != "" {
		stopThinkingBlock(state, &results)
		if !state.TextBlockStarted {
			state.TextBlockIndex = state.NextBlockIndex
			state.NextBlockIndex++
			state.TextBlockStarted = true
			state.TextBlockClosed = false
			results = append(results, map[string]any{
				"type":  "content_block_start",
				"index": state.TextBlockIndex,
				"content_block": map[string]any{
					"type": "text",
					"text": "",
				},
			})
		}
		results = append(results, map[string]any{
			"type":  "content_block_delta",
			"index": state.TextBlockIndex,
			"delta": map[string]any{
				"type": "text_delta",
				"text": delta.Content,
			},
		})
	}

	// 4. Tool calls
	for _, tc := range delta.ToolCalls {
		idx := 0
		if tc.Index != nil {
			idx = *tc.Index
		}
		if tc.ID != "" {
			stopThinkingBlock(state, &results)
			stopTextBlock(state, &results)
			toolBlockIndex := state.NextBlockIndex
			state.NextBlockIndex++
			toolName := tc.ID
			if tc.Function != nil && tc.Function.Name != "" {
				toolName = tc.Function.Name
			}
			toolName, _ = strings.CutPrefix(toolName, "proxy_")
			state.ToolCalls[idx] = ToolCallState{
				ID:         tc.ID,
				Name:       toolName,
				BlockIndex: toolBlockIndex,
			}
			results = append(results, map[string]any{
				"type":  "content_block_start",
				"index": toolBlockIndex,
				"content_block": map[string]any{
					"type":  "tool_use",
					"id":    tc.ID,
					"name":  toolName,
					"input": map[string]any{},
				},
			})
		}
		if tc.Function != nil && tc.Function.Arguments != "" {
			state.ToolArgBuffers[idx] = state.ToolArgBuffers[idx] + tc.Function.Arguments
		}
	}

	// 5. Finish reason
	if choice.FinishReason != nil {
		stopThinkingBlock(state, &results)
		stopTextBlock(state, &results)
		for idx, toolInfo := range state.ToolCalls {
			buffered := state.ToolArgBuffers[idx]
			sanitized := sanitizeToolArgs(toolInfo.Name, buffered)
			results = append(results, map[string]any{
				"type":  "content_block_delta",
				"index": toolInfo.BlockIndex,
				"delta": map[string]any{
					"type":         "input_json_delta",
					"partial_json": sanitized,
				},
			})
			results = append(results, map[string]any{
				"type":  "content_block_stop",
				"index": toolInfo.BlockIndex,
			})
		}
		finishReason := *choice.FinishReason
		state.FinishReason = finishReason
		claudeStop := "end_turn"
		switch finishReason {
		case "stop":
			claudeStop = "end_turn"
		case "length":
			claudeStop = "max_tokens"
		case "tool_calls":
			claudeStop = "tool_use"
		}
		finalUsage := map[string]any{
			"input_tokens":  0,
			"output_tokens": 0,
		}
		if state.Usage != nil {
			finalUsage["input_tokens"] = state.Usage.PromptTokens
			finalUsage["output_tokens"] = state.Usage.CompletionTokens
		}
		results = append(results, map[string]any{
			"type": "message_delta",
			"delta": map[string]any{
				"stop_reason":   claudeStop,
				"stop_sequence": nil,
			},
			"usage": finalUsage,
		})
		results = append(results, map[string]any{
			"type": "message_stop",
		})
		if state.Usage != nil {
			SetLastUsage(state.Usage)
		}
		statesMu.Lock()
		delete(states, stateKey)
		statesMu.Unlock()
	}

	var buf bytes.Buffer
	for _, res := range results {
		buf.WriteString(formatSSE(res))
	}
	return buf.Bytes(), nil
}
