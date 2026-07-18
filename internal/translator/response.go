package translator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

type StreamState struct {
	MessageStartSent     bool
	MessageId            string
	Model                string
	NextBlockIndex       int
	TextBlockStarted     bool
	TextBlockClosed      bool
	TextBlockIndex       int
	ThinkingBlockStarted bool
	ThinkingBlockIndex   int
	ToolCalls            map[int]ToolCallState
	ToolArgBuffers       map[int]string
	FinishReason         string
	Usage                *OpenAIUsage
}

type ToolCallState struct {
	ID         string
	Name       string
	BlockIndex int
}

type OpenAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	CachedTokens     int `json:"cached_tokens"`
}

type OpenAIChunk struct {
	ID      string         `json:"id"`
	Model   string         `json:"model"`
	Choices []OpenAIChoice `json:"choices"`
	Usage   *OpenAIUsage   `json:"usage"`
}

type OpenAIChoice struct {
	Index        int         `json:"index"`
	Delta        OpenAIDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

type OpenAIDelta struct {
	Role             string                 `json:"role"`
	Content          string                 `json:"content"`
	ReasoningContent string                 `json:"reasoning_content"`
	Reasoning        string                 `json:"reasoning"`
	ToolCalls        []OpenAIToolCallStream `json:"tool_calls"`
}

type OpenAIToolCallStream struct {
	Index    *int                  `json:"index"`
	ID       string                `json:"id,omitempty"`
	Type     string                `json:"type,omitempty"`
	Function *OpenAIFunctionStream `json:"function,omitempty"`
}

type OpenAIFunctionStream struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

var (
	statesMu sync.Mutex
	states   = make(map[string]*StreamState)
)

// GetStreamUsage returns the accumulated usage for a stream session and removes it.
func GetStreamUsage(sessionKey string) *OpenAIUsage {
	statesMu.Lock()
	defer statesMu.Unlock()
	if state, ok := states[sessionKey]; ok && state.Usage != nil {
		usage := *state.Usage
		return &usage
	}
	return nil
}

// GetLastStreamUsage returns usage from any completed stream session.
// Called after handleStreamResponse to capture token counts.
var lastUsage *OpenAIUsage
var lastUsageMu sync.Mutex

func SetLastUsage(u *OpenAIUsage) {
	lastUsageMu.Lock()
	defer lastUsageMu.Unlock()
	lastUsage = u
}

func GetAndClearLastUsage() *OpenAIUsage {
	lastUsageMu.Lock()
	defer lastUsageMu.Unlock()
	u := lastUsage
	lastUsage = nil
	return u
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

func sanitizeToolArgs(toolName, argsJSON string) string {
	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return argsJSON
	}

	name := toolName
	if strings.HasPrefix(name, "proxy_") {
		name = strings.TrimPrefix(name, "proxy_")
	}

	if name == "Read" {
		sanitizeReadArgs(args)
	}

	sanitized, err := json.Marshal(args)
	if err != nil {
		return argsJSON
	}
	return string(sanitized)
}

func sanitizeReadArgs(args map[string]any) {
	if limitVal, ok := args["limit"]; ok {
		switch v := limitVal.(type) {
		case string:
			var n int
			if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
				args["limit"] = n
			}
		}
		if limitNum, ok := args["limit"].(float64); ok {
			n := int(limitNum)
			if n > 2000 {
				args["limit"] = 2000
			} else if n < 1 {
				delete(args, "limit")
			} else {
				args["limit"] = n
			}
		} else if limitNum, ok := args["limit"].(int); ok {
			if limitNum > 2000 {
				args["limit"] = 2000
			} else if limitNum < 1 {
				delete(args, "limit")
			}
		}
	}

	if offsetVal, ok := args["offset"]; ok {
		switch v := offsetVal.(type) {
		case string:
			var n int
			if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
				args["offset"] = n
			}
		}
		if offsetNum, ok := args["offset"].(float64); ok {
			n := int(offsetNum)
			if n < 0 {
				args["offset"] = 0
			} else {
				args["offset"] = n
			}
		} else if offsetNum, ok := args["offset"].(int); ok {
			if offsetNum < 0 {
				args["offset"] = 0
			}
		}
	}

	if pagesVal, ok := args["pages"]; ok {
		filePath, _ := args["file_path"].(string)
		pages, _ := pagesVal.(string)
		if !isValidPdfPagesArg(filePath, pages) {
			delete(args, "pages")
		}
	}
}

func isValidPdfPagesArg(filePath, pages string) bool {
	if filePath == "" || pages == "" {
		return false
	}
	filePathLower := strings.ToLower(filePath)
	if !strings.HasSuffix(filePathLower, ".pdf") {
		return false
	}
	matched, _ := regexp.MatchString(`^\d+(-\d+)?$`, pages)
	return matched
}

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

	// Update usage if present
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
	}

	var results []map[string]any

	// 1. Message Start Event
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
		// Just output formatting if we have any results (e.g. message_start)
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

	// 2. Handle Reasoning Content
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

	// 3. Handle Regular Content
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

	// 4. Handle Tool Calls
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
			if strings.HasPrefix(toolName, "proxy_") {
				toolName = strings.TrimPrefix(toolName, "proxy_")
			}

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

	// 5. Handle Finish Reason
	if choice.FinishReason != nil {
		stopThinkingBlock(state, &results)
		stopTextBlock(state, &results)

		// Emit all buffered tool call inputs
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

		// Capture usage before cleanup
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
