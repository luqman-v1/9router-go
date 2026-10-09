package translator

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"strings"
	"time"
)

// extractReasoningText pulls reasoning out of a Chat delta across the three
// shapes vendors use: reasoning_content, reasoning, and reasoning_details.
func extractReasoningText(delta OpenAIDelta) string {
	if delta.ReasoningContent != "" {
		return delta.ReasoningContent
	}
	if delta.Reasoning != "" {
		return delta.Reasoning
	}

	var b strings.Builder
	for _, detail := range delta.ReasoningDetails {
		switch {
		case detail.Text != "":
			b.WriteString(detail.Text)
		case detail.Content != "":
			b.WriteString(detail.Content)
		}
	}
	return b.String()
}

func formatSSE(event map[string]any) string {
	eventType, _ := event["type"].(string)
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Sprintf("event: %s\ndata: {}\n\n", eventType)
	}
	return fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, string(payload))
}

// CachedTokensFromJSON reads cached prompt tokens from every persisted and
// provider-facing compatibility shape. It returns the first explicit numeric
// value in precedence order and never treats JSON null as a value.
func CachedTokensFromJSON(raw []byte) int {
	if len(raw) == 0 {
		return 0
	}
	var usage struct {
		CachedTokens         *float64 `json:"cached_tokens"`
		CacheReadInputTokens *float64 `json:"cache_read_input_tokens"`
		// Ollama's native shape: prompt_eval_cached_count is a cache-read
		// SUBSET of prompt_eval_count, which Ollama reports cache-INCLUSIVE
		// — the same convention OpenAI and Gemini use. It is not subtracted
		// from the prompt total here; recording it is what lets the cache
		// analytics and the pricing tables see a real cache hit rate.
		PromptEvalCachedCount *float64 `json:"prompt_eval_cached_count"`
		PromptTokensDetails   *struct {
			CachedTokens *float64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		InputTokensDetails *struct {
			CachedTokens *float64 `json:"cached_tokens"`
		} `json:"input_tokens_details"`
	}
	if json.Unmarshal(raw, &usage) != nil {
		return 0
	}
	var promptCached *float64
	if usage.PromptTokensDetails != nil {
		promptCached = usage.PromptTokensDetails.CachedTokens
	}
	var inputCached *float64
	if usage.InputTokensDetails != nil {
		inputCached = usage.InputTokensDetails.CachedTokens
	}
	for _, candidate := range []*float64{
		usage.CachedTokens,
		usage.CacheReadInputTokens,
		promptCached,
		inputCached,
		usage.PromptEvalCachedCount,
	} {
		if candidate != nil && *candidate >= 0 {
			return int(*candidate)
		}
	}
	return 0
}

// NormalizeClaudeUsage converts a fresh Claude usage record to the canonical
// OpenAI shape. The source marker makes repeated calls safe.
func NormalizeClaudeUsage(usage *OpenAIUsage) *OpenAIUsage {
	if usage == nil || usage.PromptCacheIncluded {
		return usage
	}
	usage.PromptTokens += usage.GetCachedTokens() + usage.CacheCreationInputTokens
	usage.PromptCacheIncluded = true
	return usage
}

// ParseClaudeUsage extracts usage from a Claude-format response body. Claude
// prompt cache counters are separate from input_tokens, so canonical OpenAI
// prompt_tokens must include cache reads and creation.
func ParseClaudeUsage(body []byte) *OpenAIUsage {
	var raw struct {
		Usage jsontext.Value `json:"usage"`
	}
	if json.Unmarshal(body, &raw) != nil || len(raw.Usage) == 0 || string(raw.Usage) == "null" {
		return nil
	}
	var keys map[string]jsontext.Value
	if json.Unmarshal(raw.Usage, &keys) != nil {
		return nil
	}
	if _, ok := keys["input_tokens"]; !ok {
		return nil
	}
	var u struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	}
	if err := json.Unmarshal(raw.Usage, &u); err != nil {
		return nil
	}
	return NormalizeClaudeUsage(&OpenAIUsage{
		PromptTokens:             u.InputTokens,
		CompletionTokens:         u.OutputTokens,
		CachedTokens:             CachedTokensFromJSON(raw.Usage),
		CacheCreationInputTokens: u.CacheCreationInputTokens,
	})
}

// ParseResponseUsage extracts usage from an OpenAI or Claude response body.
func ParseResponseUsage(body []byte) *OpenAIUsage {
	if u := ParseClaudeUsage(body); u != nil {
		return u
	}
	var raw struct {
		Usage jsontext.Value `json:"usage"`
	}
	if json.Unmarshal(body, &raw) != nil || len(raw.Usage) == 0 || string(raw.Usage) == "null" {
		return nil
	}
	var usage OpenAIUsage
	if json.Unmarshal(raw.Usage, &usage) != nil {
		return nil
	}
	usage.CachedTokens = CachedTokensFromJSON(raw.Usage)
	usage.PromptCacheIncluded = true
	return &usage
}

// ParseOllamaUsage extracts usage from an Ollama native response body, whose
// counters sit at the TOP level rather than under `usage`.
//
// It returns nil for a body that is not an Ollama completion: the `done` flag is
// what distinguishes the final chunk from the intermediate NDJSON lines, and a
// false positive here would fabricate a usage record for an OpenAI-shaped body.
func ParseOllamaUsage(body []byte) *OpenAIUsage {
	var raw struct {
		Done                  bool `json:"done"`
		PromptEvalCount       *int `json:"prompt_eval_count"`
		EvalCount             *int `json:"eval_count"`
		PromptEvalCachedCount *int `json:"prompt_eval_cached_count"`
	}
	if json.Unmarshal(body, &raw) != nil || !raw.Done {
		return nil
	}
	if raw.PromptEvalCount == nil && raw.EvalCount == nil {
		// `done` also ends a turn that published no counters at all. Nothing
		// to account for.
		return nil
	}
	prompt, completion := 0, 0
	if raw.PromptEvalCount != nil {
		prompt = *raw.PromptEvalCount
	}
	if raw.EvalCount != nil {
		completion = *raw.EvalCount
	}
	usage := &OpenAIUsage{
		PromptTokens:        prompt,
		CompletionTokens:    completion,
		PromptCacheIncluded: true,
	}
	if raw.PromptEvalCachedCount != nil {
		usage.CachedTokens = *raw.PromptEvalCachedCount
	}
	return usage
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

	// Tool calls — don't fallback to ID when Function.Name is empty (see decolua/9router#2077, #3685)
	// Using call_ ID as tool name causes "No such tool available: call_..." in Claude Code.
	for _, tc := range msg.ToolCalls {
		if tc.Function == nil || tc.Function.Name == "" {
			continue
		}
		toolName := UncloakToolName(tc.Function.Name, nil)
		if toolName == "" {
			continue
		}
		var input jsontext.Value
		if tc.Function != nil && tc.Function.Arguments != "" {
			sanitized := sanitizeToolArgs(toolName, tc.Function.Arguments)
			input = jsontext.Value(sanitized)
		} else {
			input = jsontext.Value("{}")
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
		case "content_filter":
			claudeStop = "refusal"
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
		PromptCacheIncluded:      true,
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
			dataPart = append(prev.data, dataPart...)
			delete(pendingJSON, sessionKey)
		}
		statesMu.Unlock()
	}

	var chunk OpenAIChunk
	if err := json.Unmarshal(dataPart, &chunk); err != nil {
		if sessionKey != "" && isTruncatedJSON(err) && len(dataPart) < maxPendingJSON {
			statesMu.Lock()
			pendingJSON[sessionKey] = pendingFragment{data: dataPart, createdAt: time.Now()}
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
			// If we already have this tool idx, don't create a new block — it's a delta for the same tool (e.g. Codex streaming where first chunk has name, second has arguments with same id)
			if _, exists := state.ToolCalls[idx]; exists {
				// Already have this tool, skip block creation, just buffer arguments below
			} else {
				// New tool — don't fallback to ID when Function.Name is empty (decolua/9router#2077)
				if tc.Function == nil || tc.Function.Name == "" {
					continue
				}
				toolName := UncloakToolName(tc.Function.Name, nil)
				if toolName == "" {
					continue
				}
				stopThinkingBlock(state, &results)
				stopTextBlock(state, &results)
				toolBlockIndex := state.NextBlockIndex
				state.NextBlockIndex++
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
		case "content_filter":
			claudeStop = "refusal"
		}
		finalUsage := map[string]any{
			"input_tokens":  0,
			"output_tokens": 0,
		}
		if state.Usage != nil {
			finalUsage["input_tokens"] = state.Usage.PromptTokens
			finalUsage["output_tokens"] = state.Usage.CompletionTokens
			if cached := state.Usage.GetCachedTokens(); cached > 0 {
				finalUsage["cache_read_input_tokens"] = cached
			}
			if state.Usage.CacheCreationInputTokens > 0 {
				finalUsage["cache_creation_input_tokens"] = state.Usage.CacheCreationInputTokens
			}
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

// EnsureStreamClosed generates closing events (message_delta, message_stop) if a stream
// was interrupted or ended without an explicit finish_reason from upstream.
func EnsureStreamClosed(sessionKey string) []byte {
	if sessionKey == "" {
		return nil
	}
	statesMu.Lock()
	state, exists := states[sessionKey]
	statesMu.Unlock()
	if !exists || state == nil {
		return nil
	}
	if state.FinishReason != "" {
		return nil // Already cleanly closed
	}

	state.FinishReason = "stop"
	var results []map[string]any

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
			"stop_reason":   "end_turn",
			"stop_sequence": nil,
		},
		"usage": finalUsage,
	})
	results = append(results, map[string]any{
		"type": "message_stop",
	})

	var buf bytes.Buffer
	for _, res := range results {
		buf.WriteString(formatSSE(res))
	}
	return buf.Bytes()
}

// UnwrapClineEnvelope unwraps {"success":true,"data":{...}} envelope from Cline/Clinepass
// non-streaming responses (parity with open-sse/shared/clineEnvelope.js #122f23ee).
func UnwrapClineEnvelope(body []byte) []byte {
	var env struct {
		Success bool           `json:"success"`
		Data    jsontext.Value `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err == nil && env.Success && len(env.Data) > 0 && env.Data[0] == '{' {
		return []byte(env.Data)
	}
	return body
}
