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
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Sprintf("event: %s\ndata: {}\n\n", eventType)
	}
	return fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, string(payload))
}

// ParseClaudeUsage extracts usage from a Claude-format response body. Maps
// Claude's input_tokens/output_tokens/cache_read_input_tokens to OpenAIUsage.
// Returns nil when the body has no Claude usage (e.g. OpenAI format or error),
// so callers never stamp a bogus all-zero usage over a real one.
func ParseClaudeUsage(body []byte) *OpenAIUsage {
	var raw struct {
		Usage json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(body, &raw) != nil || len(raw.Usage) == 0 || string(raw.Usage) == "null" {
		return nil
	}
	// Only treat it as Claude usage when the defining Claude key is present.
	// An OpenAI-format body (prompt_tokens/completion_tokens) must yield nil,
	// not an all-zero usage.
	var keys map[string]json.RawMessage
	if json.Unmarshal(raw.Usage, &keys) != nil {
		return nil
	}
	if _, ok := keys["input_tokens"]; !ok {
		return nil
	}
	var u struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	}
	if err := json.Unmarshal(raw.Usage, &u); err != nil {
		return nil
	}
	return &OpenAIUsage{
		PromptTokens:             u.InputTokens,
		CompletionTokens:         u.OutputTokens,
		CachedTokens:             u.CacheReadInputTokens,
		CacheCreationInputTokens: u.CacheCreationInputTokens,
	}
}

// ParseResponseUsage extracts usage from a response body in either OpenAI or
// Claude format. The !translate path serves both /v1/chat/completions (OpenAI
// bodies, translateResponse hardcoded false) and claude/anthropic providers
// (Claude bodies), so a single-format parser would silently drop usage.
func ParseResponseUsage(body []byte) *OpenAIUsage {
	if u := ParseClaudeUsage(body); u != nil {
		return u
	}
	var raw struct {
		Usage *OpenAIUsage `json:"usage"`
	}
	if json.Unmarshal(body, &raw) == nil && raw.Usage != nil {
		return raw.Usage
	}
	return nil
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

// TranslateOpenAIToClaude translates an OpenAI non-streaming response into a Claude non-streaming response.
func TranslateOpenAIToClaude(openaiResp []byte) ([]byte, *OpenAIUsage, error) {
	trimmed := bytes.TrimSpace(openaiResp)
	if len(trimmed) == 0 {
		return nil, nil, fmt.Errorf("empty response body")
	}

	var resp OpenAIResponse
	if err := json.Unmarshal(openaiResp, &resp); err != nil {
		return nil, nil, fmt.Errorf("failed to parse OpenAI response: %w", err)
	}

	if len(resp.Choices) == 0 {
		return nil, nil, fmt.Errorf("no choices in OpenAI response")
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
	inputTokens, outputTokens, cachedTokens, cacheCreationTokens := 0, 0, 0, 0
	var details *CompletionTokensDetails
	if resp.Usage != nil {
		inputTokens = resp.Usage.PromptTokens
		outputTokens = resp.Usage.CompletionTokens
		details = resp.Usage.CompletionTokensDetails
		cachedTokens = resp.Usage.GetCachedTokens()
		cacheCreationTokens = resp.Usage.CacheCreationInputTokens
	}
	usage := &OpenAIUsage{
		PromptTokens:             inputTokens,
		CompletionTokens:         outputTokens,
		CachedTokens:             cachedTokens,
		CacheCreationInputTokens: cacheCreationTokens,
		CompletionTokensDetails:  details,
	}
	claudeUsageMap := map[string]any{
		"input_tokens":  inputTokens,
		"output_tokens": outputTokens,
	}
	if cachedTokens > 0 {
		claudeUsageMap["cache_read_input_tokens"] = cachedTokens
	}
	if cacheCreationTokens > 0 {
		claudeUsageMap["cache_creation_input_tokens"] = cacheCreationTokens
	}

	result := map[string]any{
		"id":            msgID,
		"type":          "message",
		"role":          "assistant",
		"model":         modelName,
		"content":       contentBlocks,
		"stop_reason":   claudeStop,
		"stop_sequence": nil,
		"usage":         claudeUsageMap,
	}

	out, err := json.Marshal(result)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal Claude response: %w", err)
	}
	return out, usage, nil
}

// TranslateOpenAIToClaudeStream converts a single OpenAI SSE chunk JSON payload to Claude SSE format.
// It keys the per-stream translation state by chunk.ID; callers with concurrent
// streams should prefer TranslateOpenAIToClaudeStreamSession to scope state per request.
func TranslateOpenAIToClaudeStream(openaiChunk []byte) ([]byte, error) {
	return TranslateOpenAIToClaudeStreamSession("", openaiChunk)
}

// TranslateOpenAIToClaudeStreamSession is like TranslateOpenAIToClaudeStream but
// scopes the translation state to sessionKey instead of chunk.ID. A stream that
// spans multiple calls MUST pass the same sessionKey every time, and SHOULD
// defer ClearStreamState(sessionKey) after the stream ends so state cannot
// collide with another request or leak.
func TranslateOpenAIToClaudeStreamSession(sessionKey string, openaiChunk []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(openaiChunk)
	if len(trimmed) == 0 {
		return nil, nil
	}
	if string(trimmed) == "[DONE]" {
		return []byte("data: [DONE]\n\n"), nil
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

	// Some upstreams (opencode free-tier) split one JSON object across multiple
	// SSE events. Rejoin any buffered tail of a previously-truncated payload
	// before parsing. Only possible with a stable sessionKey.
	if sessionKey != "" {
		statesMu.Lock()
		if prev, ok := pendingJSON[sessionKey]; ok {
			dataPart = append(prev, dataPart...)
			delete(pendingJSON, sessionKey)
		}
		statesMu.Unlock()
	}

	var chunk OpenAIChunk
	if err := json.Unmarshal(dataPart, &chunk); err != nil {
		if sessionKey != "" && isTruncatedJSON(err) && len(dataPart) < maxPendingJSON {
			statesMu.Lock()
			pendingJSON[sessionKey] = dataPart
			statesMu.Unlock()
			return nil, nil // hold the fragment until the continuation arrives
		}
		return nil, fmt.Errorf("unmarshal stream chunk: %w", err)
	}

	stateKey := sessionKey
	if stateKey == "" {
		stateKey = chunk.ID
	}
	if stateKey == "" {
		stateKey = "default-session"
	}

	statesMu.Lock()
	pruneStaleStatesLocked()
	state, exists := states[stateKey]
	if !exists {
		// Zero-choice sidecars (OpenCode inference-cost, usage-only after finish)
		// must not bootstrap a fake Claude message.
		if len(chunk.Choices) == 0 {
			statesMu.Unlock()
			return nil, nil
		}
		cleanID := strings.Replace(chunk.ID, "chatcmpl-", "", 1)
		if cleanID == "" || cleanID == "chat" {
			cleanID = fmt.Sprintf("msg_%d", time.Now().UnixNano())
		}
		modelName := chunk.Model
		if modelName == "" {
			modelName = "claude-3-5-sonnet"
		}
		state = &StreamState{
			CreatedAt:      time.Now(),
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
		if cached := chunk.Usage.GetCachedTokens(); cached > 0 {
			state.Usage.CachedTokens = cached
		}
		if chunk.Usage.CacheCreationInputTokens > 0 {
			state.Usage.CacheCreationInputTokens = chunk.Usage.CacheCreationInputTokens
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
		// NOTE: the state is intentionally left in the map here so callers can
		// still read accumulated usage via GetStreamUsage(sessionKey) after the
		// finish chunk. Cleanup is the caller's job via ClearStreamState (defer).
	}

	var buf bytes.Buffer
	for _, res := range results {
		buf.WriteString(formatSSE(res))
	}
	return buf.Bytes(), nil
}
