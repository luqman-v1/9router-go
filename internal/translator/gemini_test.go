package translator

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestThoughtSignatureResponseRoundTrip(t *testing.T) {
	// ── 1. Gemini Response → OpenAI (thought_sig di-encode ke tool call ID) ──
	geminiResp := `{
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{
					"text": "Let me check the weather."
				}, {
					"functionCall": {
						"name": "get_weather",
						"args": {"location": "Jakarta"}
					},
					"thought_signature": "EvEFCu4FAQw..."
				}]
			},
			"finishReason": "STOP"
		}],
		"usageMetadata": {
			"promptTokenCount": 10,
			"candidatesTokenCount": 20
		}
	}`

	openaiBytes, usage, err := TranslateGeminiResponseToOpenAI([]byte(geminiResp))
	if err != nil {
		t.Fatalf("TranslateGeminiResponseToOpenAI failed: %v", err)
	}
	if usage == nil {
		t.Fatal("expected non-nil usage")
	}

	// Parse OpenAI response
	var openaiResp struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(openaiBytes, &openaiResp); err != nil {
		t.Fatalf("unmarshal openai response: %v", err)
	}

	if len(openaiResp.Choices) == 0 {
		t.Fatal("expected at least 1 choice")
	}
	msg := openaiResp.Choices[0].Message
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool_call, got %d", len(msg.ToolCalls))
	}
	tc := msg.ToolCalls[0]

	if tc.Function.Name != "get_weather" {
		t.Errorf("expected function name get_weather, got %s", tc.Function.Name)
	}
	if !strings.Contains(tc.ID, "__ts__") {
		t.Errorf("tool call ID missing __ts__ encoding: %s", tc.ID)
	}
	if !strings.HasSuffix(tc.ID, "__ts__EvEFCu4FAQw...") {
		t.Errorf("expected thought_sig suffix in tool call ID, got %s", tc.ID)
	}
	if tc.Type != "function" {
		t.Errorf("expected type function, got %s", tc.Type)
	}

	// ── 2. OpenAI Request (echo back) → Gemini (thought_sig di-decode) ──
	// Simulate what a client sends back: the tool call echoed in assistant message
	openaiReq := `{
		"model": "gemini-3.5-flash",
		"messages": [
			{"role": "user", "content": "What is the weather in Jakarta?"},
			{
				"role": "assistant",
				"content": "Let me check the weather.",
				"tool_calls": [
					{
						"id": "call_get_weather_0__ts__EvEFCu4FAQw...",
						"type": "function",
						"function": {
							"name": "get_weather",
							"arguments": "{\"location\":\"Jakarta\"}"
						}
					}
				]
			},
			{
				"role": "tool",
				"tool_call_id": "get_weather",
				"content": "{\"temperature\": 32, \"unit\": \"celsius\"}"
			}
		],
		"tools": [{
			"type": "function",
			"function": {
				"name": "get_weather",
				"description": "Get weather",
				"parameters": {
					"type": "object",
					"properties": {
						"location": {"type": "string"}
					}
				}
			}
		}]
	}`

	geminiBytes, err := TranslateOpenAIToGemini([]byte(openaiReq))
	if err != nil {
		t.Fatalf("TranslateOpenAIToGemini failed: %v", err)
	}

	// Parse Gemini request
	var geminiReq struct {
		Contents []struct {
			Role  string          `json:"role"`
			Parts json.RawMessage `json:"parts"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(geminiBytes, &geminiReq); err != nil {
		t.Fatalf("unmarshal gemini request: %v", err)
	}

	// Find the assistant/model content
	var modelContent *struct {
		Role  string          `json:"role"`
		Parts json.RawMessage `json:"parts"`
	}
	for i := range geminiReq.Contents {
		if geminiReq.Contents[i].Role == "model" {
			modelContent = &geminiReq.Contents[i]
			break
		}
	}
	if modelContent == nil {
		t.Fatal("expected model role content in gemini request")
	}

	var parts []GeminiPart
	if err := json.Unmarshal(modelContent.Parts, &parts); err != nil {
		t.Fatalf("unmarshal parts: %v", err)
	}

	found := false
	for _, p := range parts {
		if p.FunctionCall != nil && p.FunctionCall.Name == "get_weather" {
			found = true
			if p.ThoughtSignature != "EvEFCu4FAQw..." {
				t.Errorf("thought_signature not decoded properly, got: %q", p.ThoughtSignature)
			}
			if p.FunctionCall.Args["location"] != "Jakarta" {
				t.Errorf("args not preserved: %v", p.FunctionCall.Args)
			}
		}
	}
	if !found {
		t.Fatal("functionCall get_weather not found in translated Gemini request")
	}
}

func TestThoughtSignatureStreamRoundTrip(t *testing.T) {
	state := &GeminiStreamState{}

	// Simulate a streaming chunk with functionCall + thought_signature
	geminiChunk := `{
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{
					"functionCall": {
						"name": "search_web",
						"args": {"query": "Gemini API docs"}
					},
					"thought_signature": "xyz123sig"
				}]
			},
			"finishReason": "STOP",
			"index": 0
		}]
	}`

	openaiChunk, err := TranslateGeminiChunkToOpenAI([]byte(geminiChunk), state)
	if err != nil {
		t.Fatalf("TranslateGeminiChunkToOpenAI failed: %v", err)
	}
	if openaiChunk == nil {
		t.Fatal("expected non-nil chunk")
	}

	// Output is SSE-format: "data: {...}\n\ndata: {...}\n\n"
	// First line is the role delta, second is the tool_calls delta
	lines := strings.Split(strings.TrimSpace(string(openaiChunk)), "\n\n")
	var toolCallLine string
	for _, line := range lines {
		if strings.Contains(line, "tool_calls") {
			toolCallLine = line
			break
		}
	}
	if toolCallLine == "" {
		t.Fatalf("no tool_calls line found in output:\n%s", string(openaiChunk))
	}

	// Strip "data: " prefix
	toolCallLine = strings.TrimPrefix(toolCallLine, "data: ")

	var parsed struct {
		Choices []struct {
			Delta struct {
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(toolCallLine), &parsed); err != nil {
		t.Fatalf("unmarshal tool_call line %q: %v", toolCallLine, err)
	}

	if len(parsed.Choices) == 0 || len(parsed.Choices[0].Delta.ToolCalls) == 0 {
		t.Fatal("expected tool_calls in stream chunk")
	}
	tc := parsed.Choices[0].Delta.ToolCalls[0]
	if !strings.HasSuffix(tc.ID, "__ts__xyz123sig") {
		t.Errorf("expected thought_sig in stream tool call ID, got %s", tc.ID)
	}
}

func TestThoughtSignatureNoSignature(t *testing.T) {
	// When thought_signature is empty, ID should be clean
	geminiResp := `{
		"candidates": [{
			"content": {
				"parts": [{
					"functionCall": {
						"name": "test_fn",
						"args": {}
					}
				}]
			},
			"finishReason": "STOP"
		}],
		"usageMetadata": {"promptTokenCount": 1, "candidatesTokenCount": 1}
	}`

	openaiBytes, _, _ := TranslateGeminiResponseToOpenAI([]byte(geminiResp))
	var resp struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					ID string `json:"id"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	json.Unmarshal(openaiBytes, &resp)

	tc := resp.Choices[0].Message.ToolCalls[0]
	if strings.Contains(tc.ID, "__ts__") {
		t.Errorf("no __ts__ expected when thought_signature is empty, got %s", tc.ID)
	}
}
