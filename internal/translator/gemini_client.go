package translator

import (
	json "encoding/json/v2"
	"fmt"
	"strings"
)

// Gemini API client direction: a Gemini SDK (the @google/genai client, the
// Python SDK, curl) posts `{contents, systemInstruction, generationConfig,
// tools}` and expects a `GenerateContentResponse` back. The gateway speaks
// OpenAI internally, so both directions have to be converted.
//
// This is the reverse of TranslateOpenAIToGemini, which serves the upstream
// side. Upstream reference: open-sse/translator/request/gemini-to-openai.js and
// response/gemini-to-openai.js.

// GeminiClientRequest is the body a Gemini SDK sends. It reuses the shared
// contents/parts types rather than declaring a parallel set: the wire names are
// identical, and two definitions would drift the first time either changed.
type GeminiClientRequest struct {
	Contents          []GeminiContent      `json:"contents"`
	SystemInstruction *GeminiContent       `json:"systemInstruction,omitempty"`
	GenerationConfig  *GeminiGenerationCfg `json:"generationConfig,omitempty"`
	Tools             []GeminiTool         `json:"tools,omitempty"`
}

// GeminiGenerationCfg is the sampling block a client sends.
type GeminiGenerationCfg struct {
	MaxOutputTokens int      `json:"maxOutputTokens,omitempty"`
	Temperature     float64  `json:"temperature,omitempty"`
	TopP            float64  `json:"topP,omitempty"`
	TopK            int      `json:"topK,omitempty"`
	StopSequences   []string `json:"stopSequences,omitempty"`
}

// GeminiClientToChatRequest converts a Gemini SDK request into the OpenAI Chat
// body the gateway routes.
func GeminiClientToChatRequest(body []byte, model string, stream bool) ([]byte, error) {
	var in GeminiClientRequest
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, fmt.Errorf("gemini client: parse request: %w", err)
	}

	out := map[string]any{
		"model":  model,
		"stream": stream,
	}
	msgs := make([]any, 0, len(in.Contents)+1)

	if in.SystemInstruction != nil {
		if text := geminiPartsText(in.SystemInstruction.Parts); strings.TrimSpace(text) != "" {
			msgs = append(msgs, map[string]any{"role": "system", "content": text})
		}
	}

	for _, content := range in.Contents {
		if len(content.Parts) == 0 {
			continue
		}
		role := "assistant"
		if content.Role == "user" || content.Role == "" {
			role = "user"
		}

		var texts []string
		var images []any
		var toolCalls []any
		var toolMsg map[string]any

		for _, part := range content.Parts {
			switch {
			case part.Text != "":
				texts = append(texts, part.Text)
			case part.InlineData != nil && part.InlineData.Data != "":
				images = append(images, map[string]any{
					"type": "image_url",
					"image_url": map[string]any{
						"url": "data:" + part.InlineData.MimeType + ";base64," + part.InlineData.Data,
					},
				})
			case part.FunctionCall != nil:
				// Gemini has no native call id, so derive one from the name. The
				// matching functionResponse derives the same id, and providers
				// require the pair to line up.
				id := part.FunctionCall.ID
				if id == "" {
					id = "call_" + part.FunctionCall.Name
				}
				args := "{}"
				if len(part.FunctionCall.Args) > 0 {
					if raw, err := json.Marshal(part.FunctionCall.Args); err == nil {
						args = string(raw)
					}
				}
				toolCalls = append(toolCalls, map[string]any{
					"id":   id,
					"type": "function",
					"function": map[string]any{
						"name":      part.FunctionCall.Name,
						"arguments": args,
					},
				})
			case part.FunctionResponse != nil:
				id := part.FunctionResponse.ID
				if id == "" {
					id = "call_" + part.FunctionResponse.Name
				}
				body := "{}"
				if part.FunctionResponse.Response != nil {
					if raw, err := json.Marshal(part.FunctionResponse.Response); err == nil {
						body = string(raw)
					}
				}
				toolMsg = map[string]any{
					"role":         "tool",
					"tool_call_id": id,
					"content":      body,
				}
			}
		}

		// A functionResponse is its own turn: the OpenAI shape pairs it with the
		// call by id, not by role.
		if toolMsg != nil {
			msgs = append(msgs, toolMsg)
			continue
		}

		msg := map[string]any{"role": role}
		switch {
		case len(images) > 0:
			blocks := make([]any, 0, len(texts)+len(images))
			if len(texts) > 0 {
				blocks = append(blocks, map[string]any{"type": "text", "text": strings.Join(texts, "\n")})
			}
			blocks = append(blocks, images...)
			msg["content"] = blocks
		case len(texts) == 1:
			msg["content"] = texts[0]
		case len(texts) > 1:
			msg["content"] = strings.Join(texts, "\n")
		default:
			msg["content"] = ""
		}
		if len(toolCalls) > 0 {
			msg["tool_calls"] = toolCalls
		}
		msgs = append(msgs, msg)
	}

	out["messages"] = msgs

	if cfg := in.GenerationConfig; cfg != nil {
		if cfg.MaxOutputTokens > 0 {
			out["max_tokens"] = cfg.MaxOutputTokens
		}
		if cfg.Temperature > 0 {
			out["temperature"] = cfg.Temperature
		}
		if cfg.TopP > 0 {
			out["top_p"] = cfg.TopP
		}
		if len(cfg.StopSequences) > 0 {
			out["stop"] = cfg.StopSequences
		}
	}

	var tools []any
	for _, t := range in.Tools {
		for _, fn := range t.FunctionDeclarations {
			params := fn.Parameters
			if params == nil {
				params = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        fn.Name,
					"description": fn.Description,
					"parameters":  params,
				},
			})
		}
	}
	if len(tools) > 0 {
		out["tools"] = tools
	}

	raw, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("gemini client: marshal chat body: %w", err)
	}
	return raw, nil
}

// geminiPartsText joins the text parts of a turn.
func geminiPartsText(parts []GeminiPart) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

// geminiFinishReasons maps OpenAI finish reasons onto Gemini's vocabulary.
var geminiFinishReasons = map[string]string{
	"stop":           "STOP",
	"length":         "MAX_TOKENS",
	"tool_calls":     "STOP",
	"content_filter": "SAFETY",
	"function_call":  "STOP",
}

// ChatResponseToGemini turns one OpenAI chat.completion into a
// GenerateContentResponse.
func ChatResponseToGemini(body []byte, model string) ([]byte, error) {
	var in struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				ToolCalls        []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, fmt.Errorf("gemini client: parse chat response: %w", err)
	}

	parts := make([]any, 0, 3)
	if len(in.Choices) > 0 {
		msg := in.Choices[0].Message
		if msg.ReasoningContent != "" {
			parts = append(parts, map[string]any{"text": msg.ReasoningContent, "thought": true})
		}
		if msg.Content != "" {
			parts = append(parts, map[string]any{"text": msg.Content})
		}
		for _, tc := range msg.ToolCalls {
			var args any = map[string]any{}
			if tc.Function.Arguments != "" {
				if json.Unmarshal([]byte(tc.Function.Arguments), &args) != nil {
					args = map[string]any{}
				}
			}
			parts = append(parts, map[string]any{
				"functionCall": map[string]any{
					"id":   tc.ID,
					"name": tc.Function.Name,
					"args": args,
				},
			})
		}
	}
	if len(parts) == 0 {
		parts = append(parts, map[string]any{"text": ""})
	}

	candidate := map[string]any{
		"content": map[string]any{"role": "model", "parts": parts},
		"index":   0,
	}
	if len(in.Choices) > 0 {
		finish := geminiFinishReasons[in.Choices[0].FinishReason]
		if finish == "" {
			finish = "STOP"
		}
		candidate["finishReason"] = finish
	}

	out := map[string]any{"candidates": []any{candidate}}
	if model != "" {
		out["modelVersion"] = model
	}
	if in.Usage.TotalTokens > 0 {
		out["usageMetadata"] = map[string]any{
			"promptTokenCount":     in.Usage.PromptTokens,
			"candidatesTokenCount": in.Usage.CompletionTokens,
			"totalTokenCount":      in.Usage.TotalTokens,
		}
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("gemini client: marshal gemini response: %w", err)
	}
	return raw, nil
}

// ChatChunkToGemini converts one OpenAI SSE chunk payload into a Gemini SSE
// data frame. It returns false when the chunk carries nothing to emit, which is
// what a role-only or empty delta is.
func ChatChunkToGemini(chunk []byte, model string) ([]byte, bool) {
	var in struct {
		Choices []struct {
			Delta struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(chunk, &in); err != nil {
		return nil, false
	}
	if len(in.Choices) == 0 {
		return nil, false
	}
	delta := in.Choices[0].Delta
	finish := in.Choices[0].FinishReason

	parts := make([]any, 0, 2)
	if delta.ReasoningContent != "" {
		parts = append(parts, map[string]any{"text": delta.ReasoningContent, "thought": true})
	}
	if delta.Content != "" {
		parts = append(parts, map[string]any{"text": delta.Content})
	}
	// A delta with neither text nor a finish signal carries nothing Gemini
	// clients can use — the role announcement is one of these.
	if len(parts) == 0 && finish == "" {
		return nil, false
	}
	if len(parts) == 0 {
		parts = append(parts, map[string]any{"text": ""})
	}

	candidate := map[string]any{
		"content": map[string]any{"role": "model", "parts": parts},
		"index":   0,
	}
	if finish != "" {
		mapped := geminiFinishReasons[finish]
		if mapped == "" {
			mapped = "STOP"
		}
		candidate["finishReason"] = mapped
	}
	out := map[string]any{"candidates": []any{candidate}}
	if finish != "" && in.Usage != nil && in.Usage.TotalTokens > 0 {
		out["usageMetadata"] = map[string]any{
			"promptTokenCount":     in.Usage.PromptTokens,
			"candidatesTokenCount": in.Usage.CompletionTokens,
			"totalTokenCount":      in.Usage.TotalTokens,
		}
	}
	if model != "" {
		out["modelVersion"] = model
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, false
	}
	return raw, true
}
