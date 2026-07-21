package translator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"9router/proxy/internal/log"
		"strings"
	"time"
)

// ─── OpenAI → Gemini Native Format ───

// GeminiStreamState holds translation state for Gemini stream chunks.
type GeminiStreamState struct {
	MessageStartSent bool
	MessageId        string
	Model            string
	Usage            *OpenAIUsage
	FinishReason     string
}

// GeminiPart is a single part in a Gemini content block.
type GeminiPart struct {
	Text             string                `json:"text,omitempty"`
	Thought          *bool                 `json:"thought,omitempty"`
	FunctionCall     *GeminiFunctionCall   `json:"functionCall,omitempty"`
	FunctionResponse *GeminiFunctionResp   `json:"functionResponse,omitempty"`
	InlineData       *GeminiInlineData     `json:"inlineData,omitempty"`
}

type GeminiFunctionCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

type GeminiFunctionResp struct {
	Name     string           `json:"name"`
	Response *GeminiFuncResp  `json:"response,omitempty"`
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
	GenerationConfig  json.RawMessage `json:"generationConfig,omitempty"`
}

// GeminiResponse is the Gemini API response body (non-stream).
type GeminiResponse struct {
	Candidates []struct {
		Content       *GeminiContent `json:"content,omitempty"`
		FinishReason  string         `json:"finishReason,omitempty"`
	} `json:"candidates"`
	UsageMetadata *struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		CachedContentToken   int `json:"cachedContentToken,omitempty"`
		CandidatesTokenDetails *struct {
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
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
	} `json:"usageMetadata,omitempty"`
}

// TranslateOpenAIToGemini converts an OpenAI-compatible request body to Gemini native format.
func TranslateOpenAIToGemini(openaiBody []byte) ([]byte, error) {
	var oreq struct {
		Model           string          `json:"model"`
		Messages        json.RawMessage `json:"messages"`
		Temperature     *float64        `json:"temperature,omitempty"`
		MaxTokens       *int            `json:"max_tokens,omitempty"`
		TopP            *float64        `json:"top_p,omitempty"`
		TopK            *int            `json:"top_k,omitempty"`
		Stream          bool            `json:"stream,omitempty"`
		Tools           json.RawMessage `json:"tools,omitempty"`
		ReasoningEffort string          `json:"reasoning_effort,omitempty"`
	}
	if err := json.Unmarshal(openaiBody, &oreq); err != nil {
		return nil, fmt.Errorf("parse OpenAI request: %w", err)
	}

	var req GeminiRequest

	// Parse messages
	var msgs []struct {
		Role             string                 `json:"role"`
		Content          interface{}            `json:"content"`
		ToolCalls        []OpenAIToolCall       `json:"tool_calls,omitempty"`
		ToolCallID       string                 `json:"tool_call_id,omitempty"`
		ReasoningContent string                 `json:"reasoning_content,omitempty"`
	}
	if err := json.Unmarshal(oreq.Messages, &msgs); err != nil {
		return nil, fmt.Errorf("parse messages: %w", err)
	}

	for _, msg := range msgs {
		switch msg.Role {
		case "system":
			content := extractContentString(msg.Content)
			if content != "" {
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

			// Reasoning content → thought part
			if msg.ReasoningContent != "" {
				t := true
				parts = append(parts, GeminiPart{Text: msg.ReasoningContent, Thought: &t})
			}

			// Text content
			if contentStr, ok := msg.Content.(string); ok && contentStr != "" {
				parts = append(parts, GeminiPart{Text: contentStr})
			} else if contentArr, ok := msg.Content.([]interface{}); ok {
				for _, item := range contentArr {
					if m, ok := item.(map[string]interface{}); ok {
						if text, ok := m["text"].(string); ok && text != "" {
							parts = append(parts, GeminiPart{Text: text})
						}
					}
				}
			}

			// Tool calls → functionCall parts
			for _, tc := range msg.ToolCalls {
				var args map[string]any
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
					args = make(map[string]any)
				}
				parts = append(parts, GeminiPart{
					FunctionCall: &GeminiFunctionCall{Name: tc.Function.Name, Args: args},
				})
			}

			if len(parts) > 0 {
				req.Contents = append(req.Contents, GeminiContent{Role: "model", Parts: parts})
			}

		case "tool":
			content := extractContentString(msg.Content)
			// Extract tool name from tool_call_id (function name prefix)
			name := msg.ToolCallID
			if idx := strings.Index(name, "_"); idx > 0 {
				name = name[idx+1:]
			}
			// Tool result content may be plain text or JSON.
			// Gemini requires the result to be valid JSON.
			var resultValue any
			if json.Valid([]byte(content)) {
				resultValue = json.RawMessage(content)
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
		for _, part := range content.Parts {
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
				toolCalls = append(toolCalls, OpenAIToolCall{
					ID:   fmt.Sprintf("call_%s_%d", part.FunctionCall.Name, len(toolCalls)),
					Type: "function",
					Function: OpenAIFunctionCall{
						Name:      part.FunctionCall.Name,
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
	inputTokens, outputTokens := 0, 0
	reasoningTokens := 0
	if geminiResp.UsageMetadata != nil {
		inputTokens = geminiResp.UsageMetadata.PromptTokenCount
		outputTokens = geminiResp.UsageMetadata.CandidatesTokenCount
		if geminiResp.UsageMetadata.CandidatesTokenDetails != nil {
			reasoningTokens = geminiResp.UsageMetadata.CandidatesTokenDetails.ReasoningTokens
		}
	}

	usage := &OpenAIUsage{
		PromptTokens:     inputTokens,
		CompletionTokens: outputTokens,
		CompletionTokensDetails: &CompletionTokensDetails{
			ReasoningTokens: reasoningTokens,
		},
	}
	SetLastUsage(usage) // still call it just in case someone else relies on it temporarily

	resp := map[string]interface{}{
		"id":     fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		"object": "chat.completion",
		"created": time.Now().Unix(),
		"model":  "gemini",
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
					delta["tool_calls"] = []map[string]interface{}{
						{
							"index": 0,
							"id":    fmt.Sprintf("call_%s_%d", part.FunctionCall.Name, time.Now().UnixNano()),
							"type":  "function",
							"function": map[string]interface{}{
								"name":      part.FunctionCall.Name,
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
								"index": 0,
								"delta": delta,
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

			inputTokens, outputTokens := 0, 0
			if geminiChunk.UsageMetadata != nil {
				inputTokens = geminiChunk.UsageMetadata.PromptTokenCount
				outputTokens = geminiChunk.UsageMetadata.CandidatesTokenCount
			}

			state.Usage = &OpenAIUsage{
				PromptTokens:     inputTokens,
				CompletionTokens: outputTokens,
			}

			results = append(results, map[string]interface{}{
				"id":      state.MessageId,
				"object":  "chat.completion.chunk",
				"created": time.Now().Unix(),
				"model":   state.Model,
				"choices": []map[string]interface{}{
					{
						"index": 0,
						"delta": map[string]interface{}{},
						"finish_reason": openAIStop,
					},
				},
				"usage": map[string]interface{}{
					"prompt_tokens":     inputTokens,
					"completion_tokens": outputTokens,
					"total_tokens":      inputTokens + outputTokens,
				},
			})
		}
	}

	if len(results) == 0 && geminiChunk.UsageMetadata != nil {
		// Just usage update chunk
		inputTokens := geminiChunk.UsageMetadata.PromptTokenCount
		outputTokens := geminiChunk.UsageMetadata.CandidatesTokenCount
		state.Usage = &OpenAIUsage{
			PromptTokens:     inputTokens,
			CompletionTokens: outputTokens,
		}
		results = append(results, map[string]interface{}{
			"id":      state.MessageId,
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   state.Model,
			"choices": []interface{}{},
			"usage": map[string]interface{}{
				"prompt_tokens":     inputTokens,
				"completion_tokens": outputTokens,
				"total_tokens":      inputTokens + outputTokens,
			},
		})
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
					if url, ok := img["url"].(string); ok && strings.HasPrefix(url, "data:") {
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
					}
				}
			}
		}
		return parts
	}
	return nil
}

// AntigravityRequest is the wrapper format for Antigravity API.
type AntigravityRequest struct {
	Project     string          `json:"project"`
	Model       string          `json:"model"`
	UserAgent   string          `json:"userAgent"`
	RequestType string          `json:"requestType"`
	RequestID   string          `json:"requestId"`
	Request     json.RawMessage `json:"request"`
}

// WrapForAntigravity wraps a standard Gemini request in Antigravity API envelope.
func WrapForAntigravity(geminiBody []byte, projectID, modelName string) ([]byte, error) {
	wrapper := AntigravityRequest{
		Project:     projectID,
		Model:       modelName,
		UserAgent:   "antigravity/ide/0.1",
		RequestType: "agent",
		RequestID:   fmt.Sprintf("agent/%s/%d/%s/%d", projectID, time.Now().UnixMilli(), modelName, 1),
		Request:     geminiBody,
	}
	out, err := json.Marshal(wrapper)
	if err != nil {
		return nil, fmt.Errorf("marshal antigravity wrapper: %w", err)
	}
	return out, nil
}

// UnwrapAntigravityResponse extracts the inner Gemini response from antigravity envelope.
func UnwrapAntigravityResponse(raw []byte) []byte {
	var envelope struct {
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		log.Warn("translator", "unmarshal envelope failed", "error", err)
		return raw // passthrough on failure
	}
	if len(envelope.Response) == 0 {
		log.Warn("translator", "empty envelope response")
		return raw // passthrough on failure
	}
	return []byte(envelope.Response)
}

