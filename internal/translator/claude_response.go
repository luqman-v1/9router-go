package translator

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"strings"
	"time"
)

// ClaudeToOpenAIStreamState holds streaming translation state from Claude to OpenAI format.
type ClaudeToOpenAIStreamState struct {
	MessageID         string
	Model             string
	Created           int64
	RoleSent          bool
	FinishReasonSent  bool
	FinishReason      string
	NextToolCallIndex int
	ToolCalls         map[int]*ClaudeStreamToolCall // key: block_index
	Usage             *OpenAIUsage
}

// ClaudeStreamToolCall tracks an in-flight tool call block.
type ClaudeStreamToolCall struct {
	Index int
	ID    string
	Name  string
}

// TranslateClaudeChunkToOpenAI converts a single Claude SSE payload into OpenAI SSE format chunk(s).
func TranslateClaudeChunkToOpenAI(payload []byte, state *ClaudeToOpenAIStreamState) ([]byte, error) {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 {
		return nil, nil
	}

	var raw map[string]any
	if err := json.Unmarshal(trimmed, &raw); err != nil {
		return nil, nil // ignore non-json or malformed SSE line (e.g. ping)
	}

	eventType, _ := raw["type"].(string)
	if eventType == "" || eventType == "ping" {
		return nil, nil
	}

	if state.Created == 0 {
		state.Created = time.Now().Unix()
	}
	if state.ToolCalls == nil {
		state.ToolCalls = make(map[int]*ClaudeStreamToolCall)
	}

	var buf bytes.Buffer

	writeChunk := func(delta map[string]any, finishReason *string, usage *OpenAIUsage) {
		chunkObj := map[string]any{
			"id":      state.MessageID,
			"object":  "chat.completion.chunk",
			"created": state.Created,
			"model":   state.Model,
			"choices": []map[string]any{
				{
					"index":         0,
					"delta":         delta,
					"finish_reason": finishReason,
				},
			},
		}
		if usage != nil {
			chunkObj["usage"] = usage
		}
		data, err := json.Marshal(chunkObj)
		if err == nil {
			buf.WriteString("data: ")
			buf.Write(data)
			buf.WriteString("\n\n")
		}
	}

	switch eventType {
	case "message_start":
		msg, _ := raw["message"].(map[string]any)
		if msg != nil {
			id, _ := msg["id"].(string)
			if id != "" {
				state.MessageID = "chatcmpl-" + id
			}
			model, _ := msg["model"].(string)
			if model != "" {
				state.Model = model
			}
			if u, ok := msg["usage"].(map[string]any); ok {
				inputTokens, _ := u["input_tokens"].(float64)
				cacheRead, _ := u["cache_read_input_tokens"].(float64)
				cacheCreate, _ := u["cache_creation_input_tokens"].(float64)
				promptTokens := int(inputTokens + cacheRead + cacheCreate)
				state.Usage = &OpenAIUsage{
					PromptTokens:             promptTokens,
					CachedTokens:             int(cacheRead),
					CacheCreationInputTokens: int(cacheCreate),
				}
			}
		}
		if state.MessageID == "" {
			state.MessageID = fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
		}
		if state.Model == "" {
			state.Model = "union-alpha"
		}
		if !state.RoleSent {
			state.RoleSent = true
			writeChunk(map[string]any{"role": "assistant"}, nil, nil)
		}

	case "content_block_start":
		idxVal, _ := raw["index"].(float64)
		blockIdx := int(idxVal)
		block, _ := raw["content_block"].(map[string]any)
		if block != nil {
			bType, _ := block["type"].(string)
			if bType == "tool_use" {
				callIdx := state.NextToolCallIndex
				state.NextToolCallIndex++
				toolID, _ := block["id"].(string)
				toolName, _ := block["name"].(string)
				state.ToolCalls[blockIdx] = &ClaudeStreamToolCall{
					Index: callIdx,
					ID:    toolID,
					Name:  toolName,
				}
				writeChunk(map[string]any{
					"tool_calls": []map[string]any{
						{
							"index": callIdx,
							"id":    toolID,
							"type":  "function",
							"function": map[string]any{
								"name":      toolName,
								"arguments": "",
							},
						},
					},
				}, nil, nil)
			}
		}

	case "content_block_delta":
		idxVal, _ := raw["index"].(float64)
		blockIdx := int(idxVal)
		delta, _ := raw["delta"].(map[string]any)
		if delta != nil {
			dType, _ := delta["type"].(string)
			switch dType {
			case "text_delta":
				if text, ok := delta["text"].(string); ok && text != "" {
					writeChunk(map[string]any{"content": text}, nil, nil)
				}
			case "thinking_delta":
				if thinking, ok := delta["thinking"].(string); ok && thinking != "" {
					writeChunk(map[string]any{"reasoning_content": thinking}, nil, nil)
				}
			case "input_json_delta":
				if partial, ok := delta["partial_json"].(string); ok {
					if tc, exists := state.ToolCalls[blockIdx]; exists {
						writeChunk(map[string]any{
							"tool_calls": []map[string]any{
								{
									"index": tc.Index,
									"id":    tc.ID,
									"function": map[string]any{
										"arguments": partial,
									},
								},
							},
						}, nil, nil)
					}
				}
			}
		}

	case "message_delta":
		if u, ok := raw["usage"].(map[string]any); ok {
			outTokens, _ := u["output_tokens"].(float64)
			if state.Usage == nil {
				state.Usage = &OpenAIUsage{}
			}
			state.Usage.CompletionTokens = int(outTokens)
		}
		if delta, ok := raw["delta"].(map[string]any); ok {
			if stopReason, ok := delta["stop_reason"].(string); ok && stopReason != "" {
				finishReason := "stop"
				switch stopReason {
				case "tool_use":
					finishReason = "tool_calls"
				case "max_tokens":
					finishReason = "length"
				case "end_turn", "stop_sequence":
					finishReason = "stop"
				case "refusal":
					// A refusal is a blocked turn, not a clean stop: without
					// this mapping the client sees finish_reason "stop" with
					// an empty message, indistinguishable from a real answer
					// (parity with decolua/9router#4210).
					finishReason = "content_filter"
				}
				// A refusal carries no content blocks at all. Surface
				// Anthropic's own explanation as message text so the client
				// shows *why* the turn is empty instead of a blank reply.
				if stopReason == "refusal" {
					if details, ok := delta["stop_details"].(map[string]any); ok {
						if explanation, ok := details["explanation"].(string); ok && explanation != "" {
							writeChunk(map[string]any{"content": explanation}, nil, nil)
						}
					}
				}
				state.FinishReason = finishReason
				state.FinishReasonSent = true
				writeChunk(map[string]any{}, &finishReason, state.Usage)
			}
		}

	case "message_stop":
		if !state.FinishReasonSent {
			finishReason := state.FinishReason
			if finishReason == "" {
				if len(state.ToolCalls) > 0 {
					finishReason = "tool_calls"
				} else {
					finishReason = "stop"
				}
			}
			state.FinishReasonSent = true
			writeChunk(map[string]any{}, &finishReason, state.Usage)
		}
		buf.WriteString("data: [DONE]\n\n")
	}

	if buf.Len() == 0 {
		return nil, nil
	}
	return buf.Bytes(), nil
}

// TranslateClaudeResponseToOpenAI converts a non-streaming Claude Messages JSON response to OpenAI format.
func TranslateClaudeResponseToOpenAI(claudeBody []byte) ([]byte, error) {
	var raw struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Role    string `json:"role"`
		Model   string `json:"model"`
		Content []struct {
			Type     string          `json:"type"`
			Text     string          `json:"text,omitempty"`
			Thinking string          `json:"thinking,omitempty"`
			ID       string          `json:"id,omitempty"`
			Name     string         `json:"name,omitempty"`
			Input    jsontext.Value `json:"input,omitempty"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(claudeBody, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal Claude response: %w", err)
	}

	var contentBuilder strings.Builder
	var reasoningBuilder strings.Builder
	var toolCalls []map[string]any

	for _, block := range raw.Content {
		switch block.Type {
		case "text":
			contentBuilder.WriteString(block.Text)
		case "thinking":
			reasoningBuilder.WriteString(block.Thinking)
		case "tool_use":
			argsStr := string(block.Input)
			if argsStr == "" {
				argsStr = "{}"
			}
			toolCalls = append(toolCalls, map[string]any{
				"id":   block.ID,
				"type": "function",
				"function": map[string]any{
					"name":      block.Name,
					"arguments": argsStr,
				},
			})
		}
	}

	finishReason := "stop"
	switch raw.StopReason {
	case "tool_use":
		finishReason = "tool_calls"
	case "max_tokens":
		finishReason = "length"
	case "end_turn", "stop_sequence":
		finishReason = "stop"
	case "refusal":
		// Blocked turn, not a clean stop (parity with decolua/9router#4210).
		finishReason = "content_filter"
	}

	msgObj := map[string]any{
		"role":    "assistant",
		"content": contentBuilder.String(),
	}
	if reasoningBuilder.Len() > 0 {
		msgObj["reasoning_content"] = reasoningBuilder.String()
	}
	if len(toolCalls) > 0 {
		msgObj["tool_calls"] = toolCalls
	}

	promptTokens := raw.Usage.InputTokens + raw.Usage.CacheReadInputTokens + raw.Usage.CacheCreationInputTokens
	respObj := map[string]any{
		"id":      "chatcmpl-" + raw.ID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   raw.Model,
		"choices": []map[string]any{
			{
				"index":         0,
				"message":       msgObj,
				"finish_reason": finishReason,
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     promptTokens,
			"completion_tokens": raw.Usage.OutputTokens,
			"total_tokens":      promptTokens + raw.Usage.OutputTokens,
		},
	}

	return json.Marshal(respObj)
}
