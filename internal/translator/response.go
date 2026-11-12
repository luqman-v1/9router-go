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

// ResolveTokensCounts extracts prompt and completion token counts following the
// upstream usage stats precedence:
// prompt: tokens.prompt_tokens || tokens.input_tokens || rowPrompt || 0
// completion: tokens.completion_tokens || tokens.output_tokens || rowCompletion || 0
func ResolveTokensCounts(raw []byte, rowPrompt, rowCompletion int) (int64, int64) {
	prompt := int64(rowPrompt)
	completion := int64(rowCompletion)
	if len(raw) == 0 {
		return prompt, completion
	}
	var usage struct {
		PromptTokens     *float64 `json:"prompt_tokens"`
		InputTokens      *float64 `json:"input_tokens"`
		CompletionTokens *float64 `json:"completion_tokens"`
		OutputTokens     *float64 `json:"output_tokens"`
	}
	if json.Unmarshal(raw, &usage) != nil {
		return prompt, completion
	}
	if usage.PromptTokens != nil && *usage.PromptTokens > 0 {
		prompt = int64(*usage.PromptTokens)
	} else if usage.InputTokens != nil && *usage.InputTokens > 0 {
		prompt = int64(*usage.InputTokens)
	}
	if usage.CompletionTokens != nil && *usage.CompletionTokens > 0 {
		completion = int64(*usage.CompletionTokens)
	} else if usage.OutputTokens != nil && *usage.OutputTokens > 0 {
		completion = int64(*usage.OutputTokens)
	}
	return prompt, completion
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
		InputTokens              int                  `json:"input_tokens"`
		OutputTokens             int                  `json:"output_tokens"`
		CacheCreationInputTokens int                  `json:"cache_creation_input_tokens"`
		CacheCreation            *claudeCacheCreation `json:"cache_creation"`
	}
	if err := json.Unmarshal(raw.Usage, &u); err != nil {
		return nil
	}
	usage := NormalizeClaudeUsage(&OpenAIUsage{
		PromptTokens:             u.InputTokens,
		CompletionTokens:         u.OutputTokens,
		CachedTokens:             CachedTokensFromJSON(raw.Usage),
		CacheCreationInputTokens: u.CacheCreationInputTokens,
	})
	u.CacheCreation.apply(usage)
	return usage
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

// AccumulateClaudeStreamUsage parses usage from Claude SSE chunks (message_start, message_delta).
// It returns a NEW OpenAIUsage on a recognised event (a copy of curr with the
// event's fields applied), and curr unchanged otherwise. The new pointer is what
// lets callers tell "this chunk changed the usage" apart from a no-op by identity.
func AccumulateClaudeStreamUsage(curr *OpenAIUsage, chunk []byte) *OpenAIUsage {
	var raw struct {
		Type    string `json:"type"`
		Message *struct {
			Usage *struct {
				InputTokens              int                  `json:"input_tokens"`
				CacheCreationInputTokens int                  `json:"cache_creation_input_tokens"`
				CacheReadInputTokens     int                  `json:"cache_read_input_tokens"`
				CacheCreation            *claudeCacheCreation `json:"cache_creation"`
			} `json:"usage"`
		} `json:"message"`
		Usage *struct {
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(chunk, &raw) != nil {
		return curr
	}
	if raw.Type != "message_start" && raw.Type != "message_delta" {
		return curr
	}
	next := OpenAIUsage{PromptCacheIncluded: true}
	if curr != nil {
		next = *curr
		next.PromptCacheIncluded = true
	}
	switch raw.Type {
	case "message_start":
		if raw.Message != nil && raw.Message.Usage != nil {
			u := raw.Message.Usage
			next.CachedTokens = u.CacheReadInputTokens
			next.CacheCreationInputTokens = u.CacheCreationInputTokens
			next.PromptTokens = u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
			u.CacheCreation.apply(&next)
		}
	case "message_delta":
		if raw.Usage != nil {
			next.CompletionTokens = raw.Usage.OutputTokens
		}
	}
	return &next
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

// claudeStopReason maps an OpenAI finish_reason to its Anthropic stop_reason.
func claudeStopReason(finish string) string {
	switch finish {
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	case "content_filter":
		return "refusal"
	default:
		return "end_turn"
	}
}

// claudeFinalUsage renders the client-facing usage block for the terminal
// message_delta. Anthropic and OpenAI disagree on the prompt count: OpenAI's
// prompt_tokens is cache-INCLUSIVE while Anthropic's input_tokens counts only
// uncached input, with cache reads reported separately. Emitting the inclusive
// count AND cache_read_input_tokens made Claude Code (which sums them) read
// the context meter high, so the inclusive count is split here. When the
// upstream never reported prompt tokens at all (a chat upstream that ignores
// stream_options.include_usage), the seeded body-size estimate stands in so
// the client's meter still moves and auto-compact can fire.
func claudeFinalUsage(state *StreamState) map[string]any {
	input, output, cached, cacheCreation := 0, 0, 0, 0
	if state.Usage != nil {
		output = state.Usage.CompletionTokens
		cached = state.Usage.GetCachedTokens()
		cacheCreation = state.Usage.CacheCreationInputTokens
		input = state.Usage.PromptTokens - cached
		if input < 0 {
			input = 0
		}
	}
	if input == 0 && state.PromptEstimate > 0 {
		input = state.PromptEstimate
	}
	finalUsage := map[string]any{
		"input_tokens":  input,
		"output_tokens": output,
	}
	if cached > 0 {
		finalUsage["cache_read_input_tokens"] = cached
	}
	if cacheCreation > 0 {
		finalUsage["cache_creation_input_tokens"] = cacheCreation
	}
	return finalUsage
}

// claudeTerminalEvents builds the terminal message_delta + message_stop pair.
func claudeTerminalEvents(state *StreamState) []map[string]any {
	return []map[string]any{
		{
			"type": "message_delta",
			"delta": map[string]any{
				"stop_reason":   claudeStopReason(state.FinishReason),
				"stop_sequence": nil,
			},
			"usage": claudeFinalUsage(state),
		},
		{"type": "message_stop"},
	}
}

// flushDeferredTerminal emits the terminal pair for a session that saw its
// finish_reason but whose usage chunk never arrived before the stream ended.
// It returns nil when there is nothing deferred.
func flushDeferredTerminal(sessionKey string) []byte {
	if sessionKey == "" {
		return nil
	}
	statesMu.Lock()
	defer statesMu.Unlock()
	state, ok := states[sessionKey]
	if !ok || state == nil || state.TerminalSent || state.FinishReason == "" {
		return nil
	}
	state.TerminalSent = true
	var buf bytes.Buffer
	for _, res := range claudeTerminalEvents(state) {
		buf.WriteString(formatSSE(res))
	}
	return buf.Bytes()
}

// flushDSML appends any DeepSeek DSML tool calls accumulated in the stream's
// content buffer as Claude tool_use blocks, and streams the cleaned prose as a
// text block. Called once per stream: at the finish chunk and, if the finish
// never arrived, from EnsureStreamClosed. Idempotent via DSMLBuffered.
func flushDSML(state *StreamState, results *[]map[string]any) {
	if !state.DSMLBuffered {
		// Stream ended while DSMLBuffer held a deferred split-tag probe
		// (trailing "<", "<|", "<|DSML", ...) that never completed into a
		// DSML tag: emit the held-back bytes as plain text instead of
		// dropping them at stream termination.
		if state.DSMLBuffer != "" {
			streamText(state, results, state.DSMLBuffer)
			state.DSMLBuffer = ""
		}
		return
	}
	cleaned, calls := ExtractDSMLToolCalls(state.DSMLBuffer, nil)

	if cleaned != "" {
		if !state.TextBlockStarted {
			state.TextBlockIndex = state.NextBlockIndex
			state.NextBlockIndex++
			state.TextBlockStarted = true
			state.TextBlockClosed = false
			*results = append(*results, map[string]any{
				"type":          "content_block_start",
				"index":         state.TextBlockIndex,
				"content_block": map[string]any{"type": "text", "text": ""},
			})
		}
		*results = append(*results, map[string]any{
			"type":  "content_block_delta",
			"index": state.TextBlockIndex,
			"delta": map[string]any{"type": "text_delta", "text": cleaned},
		})
		state.TextBlockClosed = true
		*results = append(*results, map[string]any{
			"type":  "content_block_stop",
			"index": state.TextBlockIndex,
		})
		state.TextBlockStarted = false
	}

	for _, call := range calls {
		toolBlockIndex := state.NextBlockIndex
		state.NextBlockIndex++
		id := call.ID
		if id == "" {
			id = fmt.Sprintf("dsml-%d", toolBlockIndex)
		}
		stopThinkingBlock(state, results)
		stopTextBlock(state, results)
		*results = append(*results, map[string]any{
			"type":  "content_block_start",
			"index": toolBlockIndex,
			"content_block": map[string]any{
				"type":  "tool_use",
				"id":    id,
				"name":  call.Name,
				"input": map[string]any{},
			},
		})
		if call.Arguments != "" {
			*results = append(*results, map[string]any{
				"type":  "content_block_delta",
				"index": toolBlockIndex,
				"delta": map[string]any{
					"type":         "input_json_delta",
					"partial_json": call.Arguments,
				},
			})
		}
		*results = append(*results, map[string]any{
			"type":  "content_block_stop",
			"index": toolBlockIndex,
		})
	}
	state.DSMLBuffered = false
}

// streamText appends text_delta events for text, creating a text content block
// on first use and stopping any in-progress thinking block first.
func streamText(state *StreamState, results *[]map[string]any, text string) {
	if text == "" {
		return
	}
	stopThinkingBlock(state, results)
	if !state.TextBlockStarted {
		state.TextBlockIndex = state.NextBlockIndex
		state.NextBlockIndex++
		state.TextBlockStarted = true
		state.TextBlockClosed = false
		*results = append(*results, map[string]any{
			"type":          "content_block_start",
			"index":         state.TextBlockIndex,
			"content_block": map[string]any{"type": "text", "text": ""},
		})
	}
	*results = append(*results, map[string]any{
		"type":  "content_block_delta",
		"index": state.TextBlockIndex,
		"delta": map[string]any{"type": "text_delta", "text": text},
	})
}

// dsmlStreamSplit splits buf into (safe-to-stream text, tail to hold back).
// It holds back a trailing '<' that could begin a DSML tag in the next chunk
// (split-tag boundary), since the tag is not decidable until the following
// bytes arrive. Everything before the held-back tail cannot be the start of a
// DSML tag and is safe to stream immediately.
func dsmlStreamSplit(buf string) (stream, hold string) {
	lt := strings.LastIndexByte(buf, '<')
	if lt < 0 {
		return buf, ""
	}
	tail := buf[lt:]
	if dsmlTagPrefix(tail) {
		return buf[:lt], tail
	}
	return buf, ""
}

// IsClaudeMessageBody reports whether the body is already an Anthropic
// Messages response (`type:"message"`, `role:"assistant"`). A Chat Completions
// body never carries that pair — its discriminator is `object:"chat.completion"`
// — so the check cannot sweep an OpenAI reply in.
func IsClaudeMessageBody(body []byte) bool {
	var probe struct {
		Type string `json:"type"`
		Role string `json:"role"`
	}
	if json.Unmarshal(bytes.TrimSpace(body), &probe) != nil {
		return false
	}
	return probe.Type == "message" && probe.Role == "assistant"
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

	// An upstream that answers the Messages wire (an anthropic-compatible node,
	// minimax-code, a claude member inside a /v1/messages combo) reaches this
	// translate=true lane with a body that is already the shape the client
	// asked for. Relay it unchanged — "no choices" here turned a good answer
	// into a 502 and pushed the combo to the next member.
	if IsClaudeMessageBody(openaiResp) {
		return openaiResp, ParseClaudeUsage(openaiResp), nil
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

	// DSML tool calls parsed from inline content (DeepSeek-family upstreams
	// emit <|DSML| invoke ...> blocks instead of structured tool_calls).
	var dsmlToolCalls []DSMLToolRequest

	// Text content — strip DeepSeek DSML tool invocation blocks out of it so
	// clients never see raw markup, and promote any invokes to tool_use blocks.
	textContent := msg.Content
	if textContent != "" {
		cleaned, dsmlCalls := extractDSMLFromContent(textContent, nil)
		if cleaned != "" {
			contentBlocks = append(contentBlocks, map[string]any{
				"type": "text",
				"text": cleaned,
			})
		}
		msg.Content = cleaned
		dsmlToolCalls = append(dsmlToolCalls, dsmlCalls...)
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
			input = SafeToolCallInput(sanitized)
		} else {
			input = jsontext.Value("{}")
		}
		contentBlocks = append(contentBlocks, map[string]any{
			"type":  "tool_use",
			"id":    SanitizeClaudeToolID(tc.ID),
			"name":  toolName,
			"input": input,
		})
	}

	// DSML tool calls parsed from inline content (DeepSeek-family upstreams emit
	// <|DSML| invoke ...> blocks instead of structured tool_calls).
	for _, call := range dsmlToolCalls {
		var input jsontext.Value
		if call.Arguments != "" {
			input = SafeToolCallInput(call.Arguments)
		} else {
			input = jsontext.Value("{}")
		}
		contentBlocks = append(contentBlocks, map[string]any{
			"type":  "tool_use",
			"id":    call.ID,
			"name":  call.Name,
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
	// Anthropic's input_tokens counts only uncached input; OpenAI's
	// prompt_tokens already includes the cached subset. Splitting them here
	// keeps Claude Code (which sums input + cache_read + cache_creation)
	// from double-counting the cached tokens.
	uncachedInput := inputTokens - cachedTokens
	if uncachedInput < 0 {
		uncachedInput = 0
	}
	claudeUsageMap := map[string]any{
		"input_tokens":  uncachedInput,
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
//
// Without a session key the deferred terminal cannot be flushed at [DONE] (a
// [DONE] payload carries no chunk ID to look the state up by), so a stream
// whose finish deferred the terminal closes only via EnsureStreamClosed —
// which also needs the key. Production callers must use the Session variant.
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
		if tail := flushDeferredTerminal(sessionKey); len(tail) > 0 {
			return append(tail, []byte("data: [DONE]\n\n")...), nil
		}
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
		if tail := flushDeferredTerminal(sessionKey); len(tail) > 0 {
			return append(tail, []byte("data: [DONE]\n\n")...), nil
		}
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

	var results []map[string]any

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
		// Deferred terminal: the finish chunk already went by without usage,
		// and this trailing chunk is where stream_options.include_usage
		// delivers it. Close the message now with the real numbers.
		if state.FinishReason != "" && !state.TerminalSent {
			state.TerminalSent = true
			results = append(results, claudeTerminalEvents(state)...)
		}
	}

	// 1. Message Start
	if !state.MessageStartSent {
		state.MessageStartSent = true
		state.PromptEstimate = promptEstimateFor(sessionKey)
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

	// 3. Content — buffer once a DeepSeek DSML marker appears so raw markup is
	// never streamed to the client; cleaned text and DSML tool calls are emitted
	// at finish instead.
	//
	// To avoid O(N^2) buffer scans on non-DSML streams, text is streamed as it
	// arrives. Only a trailing '<' that might begin a DSML tag (split across
	// chunk boundaries) is held back until the next chunk decides. Once a DSML
	// open tag is confirmed, the remainder is buffered and cleaned at finish.
	// Text already streamed is never re-emitted, so there is no duplicated
	// prose.
	if delta.Content != "" {
		if state.DSMLBuffered {
			// Already in DSML mode: hold everything for finish-time cleanup.
			state.DSMLBuffer += delta.Content
		} else {
			combined := state.DSMLBuffer + delta.Content
			if loc := dsmlAnyOpen.FindStringIndex(combined); loc != nil {
				// A DSML open tag starts at loc[0]. Stream the prose before it,
				// then hold the tag and everything after for finish-time cleanup.
				if loc[0] > 0 {
					streamText(state, &results, combined[:loc[0]])
				}
				state.DSMLBuffer = combined[loc[0]:]
				state.DSMLBuffered = true
			} else {
				// No DSML tag yet. Stream everything except a trailing '<' that
				// could begin a DSML tag in the next chunk (split boundary).
				stream, hold := dsmlStreamSplit(combined)
				if stream != "" {
					streamText(state, &results, stream)
				}
				state.DSMLBuffer = hold
			}
		}
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
					ID:         SanitizeClaudeToolID(tc.ID),
					Name:       toolName,
					BlockIndex: toolBlockIndex,
				}
				results = append(results, map[string]any{
					"type":  "content_block_start",
					"index": toolBlockIndex,
					"content_block": map[string]any{
						"type":  "tool_use",
						"id":    SanitizeClaudeToolID(tc.ID),
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
		flushDSML(state, &results)
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
		state.FinishReason = *choice.FinishReason
		if state.Usage != nil {
			// Usage arrived on the finish chunk itself: close the message now.
			state.TerminalSent = true
			results = append(results, claudeTerminalEvents(state)...)
		}
		// Usage not known yet: the terminal is deferred. With
		// stream_options.include_usage an OpenAI stream carries usage on a
		// trailing zero-choice chunk AFTER the finish chunk, so emitting the
		// message_delta here would report zeros and the client's context
		// meter would never move. The usage chunk closes it (see the usage
		// block above); a stream that ends without one is closed by the
		// [DONE] flush or EnsureStreamClosed.
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
// was interrupted or ended without an explicit terminal from upstream. That
// covers both a finish_reason that never arrived and the deferred terminal of
// a finish whose usage chunk never came.
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
	if state.TerminalSent {
		return nil // Already cleanly closed
	}

	if state.FinishReason == "" {
		state.FinishReason = "stop"
	}
	state.TerminalSent = true
	var results []map[string]any

	if !state.MessageStartSent {
		state.MessageStartSent = true
		state.PromptEstimate = promptEstimateFor(sessionKey)
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
	flushDSML(state, &results)
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

	results = append(results, claudeTerminalEvents(state)...)

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
