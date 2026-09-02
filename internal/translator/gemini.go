package translator

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"strings"
	"time"
)

// ─── OpenAI → Gemini Native Format ───

// GeminiStreamState holds translation state for Gemini stream chunks.
type GeminiStreamState struct {
	MessageStartSent     bool
	MessageId            string
	Model                string
	Usage                *OpenAIUsage
	FinishReason         string
	LastThoughtSignature string
}

// GeminiFileData represents remote or uploaded files referenced by URI.
type GeminiFileData struct {
	FileUri  string `json:"fileUri"`
	MimeType string `json:"mimeType"`
}

// GeminiPart is a single part in a Gemini content block.
type GeminiPart struct {
	Text             string              `json:"text,omitempty"`
	Thought          *bool               `json:"thought,omitempty"`
	ThoughtSignature string              `json:"thoughtSignature,omitempty"`
	FunctionCall     *GeminiFunctionCall `json:"functionCall,omitempty"`
	FunctionResponse *GeminiFunctionResp `json:"functionResponse,omitempty"`
	InlineData       *GeminiInlineData   `json:"inlineData,omitempty"`
	FileData         *GeminiFileData     `json:"fileData,omitempty"`
}

// UnmarshalJSON handles both thoughtSignature (camelCase) and thought_signature (snake_case).
func (p *GeminiPart) UnmarshalJSON(data []byte) error {
	type Alias GeminiPart
	aux := &struct {
		ThoughtSignatureSnake string `json:"thought_signature"`
		*Alias
	}{
		Alias: (*Alias)(p),
	}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	if aux.ThoughtSignatureSnake != "" && p.ThoughtSignature == "" {
		p.ThoughtSignature = aux.ThoughtSignatureSnake
	}
	return nil
}

type GeminiFunctionCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

// DefaultThinkingSignature is the hardcoded thought signature the Next.js
// reference backfills onto functionCall parts that arrive without one
// (DEFAULT_THINKING_AG_SIGNATURE from open-sse/config/defaultThinkingSignature.js).
// Gemini thinking models reject a current-turn function call that carries no
// thought_signature; this keeps combos mixing Gemini with non-Gemini models
// from 400ing. The real signature (via __ts__ transport) is preferred whenever
// it exists.
const DefaultThinkingSignature = "EuwGCukGAXLI2nxwZIq54WWSoL/YN0P3TsDZ7zRnLi8g0S4aVr2HUGxvaHKySuY6HAVzcE0GPGjXrytLIldxthSvfxgUlJh6Qa9Z+Oj5QZBlYdg6HaJ6yuY5R7waE6rdwBsRf7Ft2j3DJ9rMi9qhWFqApewYtPhls3VHtuvND3l8Rm09+lbAXQs6KKWEWrxNLKTBkfpMgXhRERc/TQRMZu1twAablm6/Zk1tsYRvfWKLsNbeKF+CCojJdXJKvnR/8Ouuoa+Y2Ti20hcW7aZIIjZDFYPU//k6Ybmhg69J/imbFai2ckhfLaisqdDkdoIiBJScTOUvYqP6AE9d4MsydSC+UlhIMk4hoP76R8vUSCZRMkjOaDXstf/QoVZKbt94wyRZgAJ1G0BqI8L5ow86kLpA4wJEtxsRGymOE4bKUvApveBakYDNM9APkf+LbtbzWSseGjoZcSlycF9iN8Q2XNYKRrHbv3Lr5Y8JjdH/5y/6SHkNehTEZugaeGnSPSyCTWto1kQgHpxdWmhkLfJGNUGLmue7Mesj4TSms4J33mRpYVhNB/J333FCqIP0hr/E7BkkjEn7yZ4X7SQlh+xKPurapsnHRwiKmtsilmEFrnTE9iQr+pMr6M29qqFNv1tr5yumbaJw8JW9sB15tNsRv+dW6BjNanbsKz7HCgKUBc8tGy+7YuhXzAfViyRefcjK7eZW0Fbyt7AbybJTKz78W8NH7ye6LAwzOebXpeZ4D43fNIt8bKh26qgduSQv/7o+pAflkuqHZ99YWgHQ8h8OkZFi3eOiSYjsjhdZ/czWOdoPI/OnqIldzMPF5YlrKBLFX8VhRKVmqgsmWf5PHGulHhMkVlS+XG2UIseGy69ARa93D78Gsa+1n1kJr7EEB7Rh+27vUMxVYLdz1yMSvE5nalTAlg/ZeG8+XQ0cHuAI3KbQpHW2Q++RdXfm5JzD5WdJZUU+Zn8t8UUn85BH4RxZLeE0qJikgSsKoYVBc6YhiMjhPgkR95ReimY4Z0xCJdRo1gjexOFeODZMpQF6Yxnoic7IrdgsFA3iePTbFnPp3IAM1fAThWhXJUn3QInUOTd5o1qmTmn6REbL15g/JQNl+dqUoPkhleeb2V3kjqp1okmO3wMZbPknR3S1LZNmlS72/iBQUm+n2b/RCn4PjmM2"

type GeminiFunctionResp struct {
	Name     string          `json:"name"`
	Response *GeminiFuncResp `json:"response,omitempty"`
}

type GeminiFuncResp struct {
	Result any `json:"result,omitempty"`
}

type GeminiInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

// GeminiContent is one message in Gemini's contents array.
type GeminiContent struct {
	Role  string       `json:"role"`
	Parts []GeminiPart `json:"parts"`
}

// GeminiTool is the tool definition format Gemini expects.
type GeminiTool struct {
	FunctionDeclarations []GeminiFunctionDecl `json:"functionDeclarations"`
}

type GeminiFunctionDecl struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Parameters  interface{} `json:"parameters,omitempty"`
}

// GeminiRequest is the full Gemini API request body.
type GeminiRequest struct {
	SystemInstruction *GeminiContent  `json:"system_instruction,omitempty"`
	Contents          []GeminiContent `json:"contents"`
	Tools             []GeminiTool    `json:"tools,omitempty"`
	ToolConfig        any             `json:"toolConfig,omitempty"`
	GenerationConfig  jsontext.Value  `json:"generationConfig,omitempty"`
}

// GeminiResponse is the Gemini API response body (non-stream).
type GeminiResponse struct {
	Candidates []struct {
		Content      *GeminiContent `json:"content,omitempty"`
		FinishReason string         `json:"finishReason,omitempty"`
	} `json:"candidates"`
	UsageMetadata *struct {
		PromptTokenCount        int `json:"promptTokenCount"`
		CandidatesTokenCount    int `json:"candidatesTokenCount"`
		CachedContentTokenCount int `json:"cachedContentTokenCount,omitempty"`
		CachedContentToken      int `json:"cachedContentToken,omitempty"`
		CandidatesTokenDetails  *struct {
			ReasoningTokens int `json:"reasoningTokens"`
		} `json:"candidatesTokenDetails,omitempty"`
	} `json:"usageMetadata,omitempty"`
}

// GeminiStreamChunk represents one SSE chunk from Gemini stream.
type GeminiStreamChunk struct {
	Candidates []struct {
		Content      *GeminiContent `json:"content,omitempty"`
		FinishReason string         `json:"finishReason,omitempty"`
		Index        int            `json:"index"`
	} `json:"candidates"`
	UsageMetadata *struct {
		PromptTokenCount        int `json:"promptTokenCount"`
		CandidatesTokenCount    int `json:"candidatesTokenCount"`
		CachedContentTokenCount int `json:"cachedContentTokenCount,omitempty"`
		CachedContentToken      int `json:"cachedContentToken,omitempty"`
	} `json:"usageMetadata,omitempty"`
}

// TranslateOpenAIToGemini converts an OpenAI-compatible request body to Gemini native format.
func TranslateOpenAIToGemini(openaiBody []byte) ([]byte, error) {
	var oreq struct {
		Model           string         `json:"model"`
		Messages        jsontext.Value `json:"messages"`
		Temperature     *float64       `json:"temperature,omitempty"`
		MaxTokens       *int           `json:"max_tokens,omitempty"`
		TopP            *float64       `json:"top_p,omitempty"`
		TopK            *int           `json:"top_k,omitempty"`
		Stream          bool           `json:"stream,omitempty"`
		Tools           jsontext.Value `json:"tools,omitempty"`
		ReasoningEffort string         `json:"reasoning_effort,omitempty"`
	}
	if err := json.Unmarshal(openaiBody, &oreq); err != nil {
		return nil, fmt.Errorf("parse OpenAI request: %w", err)
	}

	var req GeminiRequest

	// Parse messages
	var msgs []struct {
		Role             string           `json:"role"`
		Content          interface{}      `json:"content"`
		ToolCalls        []OpenAIToolCall `json:"tool_calls,omitempty"`
		ToolCallID       string           `json:"tool_call_id,omitempty"`
		ReasoningContent string           `json:"reasoning_content,omitempty"`
	}
	if err := json.Unmarshal(oreq.Messages, &msgs); err != nil {
		return nil, fmt.Errorf("parse messages: %w", err)
	}

	// Pre-map tool_call_id -> function name from assistant messages
	tcID2Name := make(map[string]string)
	for _, msg := range msgs {
		if msg.Role == "assistant" {
			for _, tc := range msg.ToolCalls {
				if tc.ID != "" && tc.Function.Name != "" {
					cleanID := tc.ID
					if ts := strings.LastIndex(cleanID, "__ts__"); ts != -1 {
						cleanID = cleanID[:ts]
					}
					tcID2Name[tc.ID] = tc.Function.Name
					tcID2Name[cleanID] = tc.Function.Name
				}
			}
		}
	}

	for _, msg := range msgs {
		switch msg.Role {
		case "system":
			content := extractContentString(msg.Content)
			if strings.TrimSpace(content) != "" {
				req.SystemInstruction = &GeminiContent{
					Parts: []GeminiPart{{Text: content}},
				}
			}

		case "user":
			parts := convertContentToGeminiParts(msg.Content)
			if len(parts) > 0 {
				req.Contents = append(req.Contents, GeminiContent{Role: "user", Parts: parts})
			}

		case "assistant":
			var parts []GeminiPart

			// Text content
			hasText := false
			if contentStr, ok := msg.Content.(string); ok && contentStr != "" {
				parts = append(parts, GeminiPart{Text: contentStr})
				hasText = true
			} else if contentArr, ok := msg.Content.([]interface{}); ok {
				for _, item := range contentArr {
					if m, ok := item.(map[string]interface{}); ok {
						if text, ok := m["text"].(string); ok && text != "" {
							parts = append(parts, GeminiPart{Text: text})
							hasText = true
						}
					}
				}
			}

			// If there is no text content and no tool calls, use reasoning_content as plain text.
			// Do NOT emit reasoning_content as a thought: true part in history because Gemini
			// requires valid cryptographic signatures on thought: true parts and rejects unsigned
			// thoughts with "Corrupted thought signature".
			if !hasText && len(msg.ToolCalls) == 0 && msg.ReasoningContent != "" {
				parts = append(parts, GeminiPart{Text: msg.ReasoningContent})
			}

			// Tool calls → functionCall parts
			for _, tc := range msg.ToolCalls {
				var args map[string]any
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
					args = make(map[string]any)
				}
				ts := extractThoughtSig(tc.ID)
				if ts == "" {
					ts = DefaultThinkingSignature
				}
				gp := GeminiPart{FunctionCall: &GeminiFunctionCall{Name: tc.Function.Name, Args: args}, ThoughtSignature: ts}
				parts = append(parts, gp)
			}

			if len(parts) > 0 {
				req.Contents = append(req.Contents, GeminiContent{Role: "model", Parts: parts})
			}

		case "tool":
			content := extractContentString(msg.Content)
			cleanID := msg.ToolCallID
			if ts := strings.LastIndex(cleanID, "__ts__"); ts != -1 {
				cleanID = cleanID[:ts]
			}
			name := tcID2Name[msg.ToolCallID]
			if name == "" {
				name = tcID2Name[cleanID]
			}
			if name == "" {
				name = cleanID
				if strings.HasPrefix(name, "call_") {
					rest := strings.TrimPrefix(name, "call_")
					if lastUnderscore := strings.LastIndex(rest, "_"); lastUnderscore > 0 {
						name = rest[:lastUnderscore]
					} else {
						name = rest
					}
				}
			}
			// Tool result content may be plain text or JSON.
			// Gemini requires the result to be valid JSON.
			var resultValue any
			if jsontext.Value(content).IsValid() {
				resultValue = jsontext.Value(content)
			} else {
				// Wrap plain text as a JSON object
				resultValue = map[string]string{"output": content}
			}
			parts := []GeminiPart{{
				FunctionResponse: &GeminiFunctionResp{
					Name:     name,
					Response: &GeminiFuncResp{Result: resultValue},
				},
			}}
			req.Contents = append(req.Contents, GeminiContent{Role: "user", Parts: parts})
		}
	}

	// Tools
	if len(oreq.Tools) > 0 {
		var openaiTools []OpenAITool
		if err := json.Unmarshal(oreq.Tools, &openaiTools); err != nil {
			return nil, fmt.Errorf("parse tools: %w", err)
		}
		var decls []GeminiFunctionDecl
		for _, t := range openaiTools {
			params := t.Function.Parameters
			params = CleanParametersSchema(params)
			decls = append(decls, GeminiFunctionDecl{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				Parameters:  params,
			})
		}
		if len(decls) > 0 {
			req.Tools = []GeminiTool{{FunctionDeclarations: decls}}
			req.ToolConfig = map[string]any{
				"functionCallingConfig": map[string]any{
					"mode": "VALIDATED",
				},
			}
		}
	}

	// Generation config
	genConfig := make(map[string]interface{})
	if oreq.Temperature != nil {
		genConfig["temperature"] = *oreq.Temperature
	}
	if oreq.MaxTokens != nil {
		genConfig["maxOutputTokens"] = *oreq.MaxTokens
	}
	if oreq.TopP != nil {
		genConfig["topP"] = *oreq.TopP
	}
	if oreq.TopK != nil {
		genConfig["topK"] = *oreq.TopK
	}
	if oreq.ReasoningEffort != "" {
		genConfig["thinkingConfig"] = map[string]interface{}{
			"thinkingBudget": effortToBudget(oreq.ReasoningEffort),
		}
	}
	if len(genConfig) > 0 {
		configJSON, err := json.Marshal(genConfig)
		if err != nil {
			return nil, fmt.Errorf("marshal generation config: %w", err)
		}
		req.GenerationConfig = configJSON
	}

	out, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal gemini request: %w", err)
	}
	return out, nil
}

// extractThoughtSig extracts a thought_signature encoded in a tool call ID (format: "...__ts__<sig>").
func extractThoughtSig(id string) string {
	const sep = "__ts__"
	if _, after, ok := strings.CutLast(id, sep); ok && after != "" {
		return after
	}
	return ""
}

// effortToBudget converts reasoning_effort string to thinking budget tokens.
func effortToBudget(effort string) int {
	switch effort {
	case "high":
		return 32000
	case "medium":
		return 16000
	default:
		return 8000
	}
}

// ─── Gemini Response → OpenAI ───

// TranslateGeminiResponseToOpenAI converts a non-stream Gemini response to OpenAI format.
func TranslateGeminiResponseToOpenAI(geminiBody []byte) ([]byte, *OpenAIUsage, error) {
	var geminiResp GeminiResponse
	if err := json.Unmarshal(geminiBody, &geminiResp); err != nil {
		return nil, nil, fmt.Errorf("parse Gemini response: %w", err)
	}
	if len(geminiResp.Candidates) == 0 {
		return nil, nil, fmt.Errorf("no candidates in Gemini response")
	}

	content := geminiResp.Candidates[0].Content
	finishReason := geminiResp.Candidates[0].FinishReason

	// Build OpenAI response
	var openaiContent string
	var reasoningContent string
	var toolCalls []OpenAIToolCall

	if content != nil {
		var lastSig string
		for _, part := range content.Parts {
			if part.ThoughtSignature != "" {
				lastSig = part.ThoughtSignature
			}
			if part.Text != "" && (part.Thought == nil || !*part.Thought) {
				if openaiContent != "" {
					openaiContent += part.Text
				} else {
					openaiContent = part.Text
				}
			}
			if part.Text != "" && part.Thought != nil && *part.Thought {
				reasoningContent += part.Text
			}
			if part.FunctionCall != nil {
				args, err := json.Marshal(part.FunctionCall.Args)
				if err != nil {
					args = []byte("{}")
				}
				fnName := UncloakToolName(part.FunctionCall.Name, nil)
				id := fmt.Sprintf("call_%s_%d", fnName, len(toolCalls))
				sig := part.ThoughtSignature
				if sig == "" {
					sig = lastSig
				}
				if sig != "" {
					id += "__ts__" + sig
				}
				toolCalls = append(toolCalls, OpenAIToolCall{
					ID:   id,
					Type: "function",
					Function: OpenAIFunctionCall{
						Name:      fnName,
						Arguments: string(args),
					},
				})
			}
		}
	}

	// Map finish reason
	claudeStop := "stop"
	switch finishReason {
	case "STOP":
		claudeStop = "stop"
	case "MAX_TOKENS":
		claudeStop = "length"
	case "SAFETY":
		claudeStop = "stop"
	case "RECITATION":
		claudeStop = "stop"
	case "FINISH_REASON_UNSPECIFIED":
		claudeStop = "stop"
	}
	if len(toolCalls) > 0 {
		claudeStop = "tool_calls"
	}

	// Usage
	inputTokens, outputTokens, cachedTokens := 0, 0, 0
	reasoningTokens := 0
	if geminiResp.UsageMetadata != nil {
		inputTokens = geminiResp.UsageMetadata.PromptTokenCount
		outputTokens = geminiResp.UsageMetadata.CandidatesTokenCount
		cachedTokens = geminiResp.UsageMetadata.CachedContentTokenCount
		if cachedTokens == 0 {
			cachedTokens = geminiResp.UsageMetadata.CachedContentToken
		}
		if geminiResp.UsageMetadata.CandidatesTokenDetails != nil {
			reasoningTokens = geminiResp.UsageMetadata.CandidatesTokenDetails.ReasoningTokens
		}
	}

	usage := &OpenAIUsage{
		PromptTokens:     inputTokens,
		CompletionTokens: outputTokens,
		CachedTokens:     cachedTokens,
		CompletionTokensDetails: &CompletionTokensDetails{
			ReasoningTokens: reasoningTokens,
		},
	}
	resp := map[string]interface{}{
		"id":      fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   "gemini",
		"choices": []map[string]interface{}{{
			"index": 0,
			"message": map[string]interface{}{
				"role":              "assistant",
				"content":           openaiContent,
				"reasoning_content": reasoningContent,
				"tool_calls":        toolCalls,
			},
			"finish_reason": claudeStop,
		}},
		"usage": map[string]interface{}{
			"prompt_tokens":     inputTokens,
			"completion_tokens": outputTokens,
			"cached_tokens":     cachedTokens,
			"completion_tokens_details": map[string]interface{}{
				"reasoning_tokens": reasoningTokens,
			},
		},
	}

	// Remove empty fields
	if reasoningContent == "" {
		delete(resp["choices"].([]map[string]interface{})[0]["message"].(map[string]interface{}), "reasoning_content")
	}
	if len(toolCalls) == 0 {
		delete(resp["choices"].([]map[string]interface{})[0]["message"].(map[string]interface{}), "tool_calls")
	}

	out, err := json.Marshal(resp)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal OpenAI response: %w", err)
	}
	return out, usage, nil
}

// TranslateGeminiChunkToOpenAI converts a Gemini SSE stream chunk to OpenAI SSE format.
func TranslateGeminiChunkToOpenAI(chunk []byte, state *GeminiStreamState) ([]byte, error) {
	if len(bytes.TrimSpace(chunk)) == 0 {
		return nil, nil
	}

	var geminiChunk GeminiStreamChunk
	if err := json.Unmarshal(chunk, &geminiChunk); err != nil {
		return nil, fmt.Errorf("parse Gemini stream chunk: %w", err)
	}

	if len(geminiChunk.Candidates) == 0 && geminiChunk.UsageMetadata == nil {
		return nil, nil
	}

	// Initialize state
	if state.MessageId == "" {
		state.MessageId = fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	}
	if state.Model == "" {
		state.Model = "gemini"
	}

	var results []map[string]interface{}

	// First chunk setup
	if !state.MessageStartSent {
		state.MessageStartSent = true
		results = append(results, map[string]interface{}{
			"id":      state.MessageId,
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   state.Model,
			"choices": []map[string]interface{}{
				{
					"index": 0,
					"delta": map[string]interface{}{
						"role": "assistant",
					},
					"finish_reason": nil,
				},
			},
		})
	}

	if len(geminiChunk.Candidates) > 0 {
		candidate := geminiChunk.Candidates[0]

		if candidate.Content != nil {
			for _, part := range candidate.Content.Parts {
				if part.ThoughtSignature != "" {
					state.LastThoughtSignature = part.ThoughtSignature
				}
				delta := map[string]interface{}{}
				if part.Text != "" && (part.Thought == nil || !*part.Thought) {
					delta["content"] = part.Text
				}
				if part.Text != "" && part.Thought != nil && *part.Thought {
					delta["reasoning_content"] = part.Text
				}
				if part.FunctionCall != nil {
					args, err := json.Marshal(part.FunctionCall.Args)
					if err != nil {
						args = []byte("{}")
					}
					fnName := UncloakToolName(part.FunctionCall.Name, nil)
					id := fmt.Sprintf("call_%s_%d", fnName, time.Now().UnixNano())
					sig := part.ThoughtSignature
					if sig == "" {
						sig = state.LastThoughtSignature
					}
					if sig != "" {
						id += "__ts__" + sig
					}
					delta["tool_calls"] = []map[string]interface{}{
						{
							"index": 0,
							"id":    id,
							"type":  "function",
							"function": map[string]interface{}{
								"name":      fnName,
								"arguments": string(args),
							},
						},
					}
				}
				if len(delta) > 0 {
					results = append(results, map[string]interface{}{
						"id":      state.MessageId,
						"object":  "chat.completion.chunk",
						"created": time.Now().Unix(),
						"model":   state.Model,
						"choices": []map[string]interface{}{
							{
								"index":         0,
								"delta":         delta,
								"finish_reason": nil,
							},
						},
					})
				}
			}
		}

		// Finish reason
		if candidate.FinishReason != "" {
			openAIStop := "stop"
			switch candidate.FinishReason {
			case "STOP":
				openAIStop = "stop"
			case "MAX_TOKENS":
				openAIStop = "length"
			case "SAFETY", "RECITATION", "OTHER":
				openAIStop = "stop"
			default:
				openAIStop = "stop"
			}

			inputTokens, outputTokens, cachedTokens := 0, 0, 0
			if geminiChunk.UsageMetadata != nil {
				inputTokens = geminiChunk.UsageMetadata.PromptTokenCount
				outputTokens = geminiChunk.UsageMetadata.CandidatesTokenCount
				cachedTokens = geminiChunk.UsageMetadata.CachedContentTokenCount
				if cachedTokens == 0 {
					cachedTokens = geminiChunk.UsageMetadata.CachedContentToken
				}
			}

			state.Usage = &OpenAIUsage{
				PromptTokens:     inputTokens,
				CompletionTokens: outputTokens,
				CachedTokens:     cachedTokens,
			}

			results = append(results, map[string]interface{}{
				"id":      state.MessageId,
				"object":  "chat.completion.chunk",
				"created": time.Now().Unix(),
				"model":   state.Model,
				"choices": []map[string]interface{}{
					{
						"index":         0,
						"delta":         map[string]interface{}{},
						"finish_reason": openAIStop,
					},
				},
				"usage": map[string]interface{}{
					"prompt_tokens":     inputTokens,
					"completion_tokens": outputTokens,
					"cached_tokens":     cachedTokens,
					"total_tokens":      inputTokens + outputTokens,
				},
			})
		}
	}

	if geminiChunk.UsageMetadata != nil {
		inputTokens := geminiChunk.UsageMetadata.PromptTokenCount
		outputTokens := geminiChunk.UsageMetadata.CandidatesTokenCount
		cachedTokens := geminiChunk.UsageMetadata.CachedContentTokenCount
		if cachedTokens == 0 {
			cachedTokens = geminiChunk.UsageMetadata.CachedContentToken
		}
		state.Usage = &OpenAIUsage{
			PromptTokens:     inputTokens,
			CompletionTokens: outputTokens,
			CachedTokens:     cachedTokens,
		}
		if len(geminiChunk.Candidates) == 0 {
			results = append(results, map[string]interface{}{
				"id":      state.MessageId,
				"object":  "chat.completion.chunk",
				"created": time.Now().Unix(),
				"model":   state.Model,
				"choices": []interface{}{},
				"usage": map[string]interface{}{
					"prompt_tokens":     inputTokens,
					"completion_tokens": outputTokens,
					"cached_tokens":     cachedTokens,
					"total_tokens":      inputTokens + outputTokens,
				},
			})
		}
	}

	if len(results) == 0 {
		return nil, nil
	}

	// Format as multiple SSE lines
	var buf bytes.Buffer
	for _, res := range results {
		payload, err := json.Marshal(res)
		if err != nil {
			continue
		}
		buf.WriteString(fmt.Sprintf("data: %s\n\n", string(payload)))
	}
	return buf.Bytes(), nil
}

// ─── Helpers ───

// extractContentString extracts a string from OpenAI message content (string or array).
func extractContentString(content interface{}) string {
	switch v := content.(type) {
	case string:
		return v
	case []interface{}:
		var parts []string
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				if text, ok := m["text"].(string); ok && text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, " ")
	default:
		return ""
	}
}

// convertContentToGeminiParts converts OpenAI message content to Gemini parts.
func convertContentToGeminiParts(content interface{}) []GeminiPart {
	switch v := content.(type) {
	case string:
		if v == "" {
			return nil
		}
		return []GeminiPart{{Text: v}}
	case []interface{}:
		var parts []GeminiPart
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				if text, ok := m["text"].(string); ok && text != "" {
					parts = append(parts, GeminiPart{Text: text})
				}
				if img, ok := m["image_url"].(map[string]interface{}); ok {
					if url, ok := img["url"].(string); ok {
						if strings.HasPrefix(url, "data:") {
							// Parse data URL: data:mimeType;base64,data
							if semi := strings.Index(url, ";"); semi > 5 {
								mimeType := url[5:semi]
								if comma := strings.Index(url, ","); comma > 0 {
									data := url[comma+1:]
									parts = append(parts, GeminiPart{
										InlineData: &GeminiInlineData{MimeType: mimeType, Data: data},
									})
								}
							}
						} else if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
							parts = append(parts, GeminiPart{
								FileData: &GeminiFileData{FileUri: url, MimeType: "image/*"},
							})
						}
					}
				}
				if audio, ok := m["input_audio"].(map[string]interface{}); ok {
					if data, ok := audio["data"].(string); ok && data != "" {
						format, _ := audio["format"].(string)
						mimeType := "audio/" + format
						if format == "mp3" {
							mimeType = "audio/mpeg"
						} else if format == "" {
							mimeType = "audio/wav"
						}
						parts = append(parts, GeminiPart{
							InlineData: &GeminiInlineData{MimeType: mimeType, Data: data},
						})
					}
				}
				if audio, ok := m["audio_url"].(map[string]interface{}); ok {
					if url, ok := audio["url"].(string); ok && strings.HasPrefix(url, "data:") {
						if semi := strings.Index(url, ";"); semi > 5 {
							mimeType := url[5:semi]
							if comma := strings.Index(url, ","); comma > 0 {
								data := url[comma+1:]
								parts = append(parts, GeminiPart{
									InlineData: &GeminiInlineData{MimeType: mimeType, Data: data},
								})
							}
						}
					}
				}
				if file, ok := m["file"].(map[string]interface{}); ok {
					if fileData, ok := file["file_data"].(string); ok && strings.HasPrefix(fileData, "data:") {
						if semi := strings.Index(fileData, ";"); semi > 5 {
							mimeType := fileData[5:semi]
							if comma := strings.Index(fileData, ","); comma > 0 {
								data := fileData[comma+1:]
								parts = append(parts, GeminiPart{
									InlineData: &GeminiInlineData{MimeType: mimeType, Data: data},
								})
							}
						}
					}
				}
			}
		}
		return parts
	}
	return nil
}
