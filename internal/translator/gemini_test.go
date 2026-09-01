package translator

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
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

	// The native Gemini generateContent endpoint only recognizes the camelCase
	// part field. The 400 "missing thought_signature" regression was caused by
	// emitting snake_case here.
	if !strings.Contains(string(geminiBytes), `"thoughtSignature":"EvEFCu4FAQw..."`) {
		t.Errorf("emitted Gemini part must use camelCase thoughtSignature, got: %s", geminiBytes)
	}
	if strings.Contains(string(geminiBytes), `"thought_signature"`) {
		t.Errorf("emitted Gemini part must NOT use snake_case thought_signature, got: %s", geminiBytes)
	}

	// Parse Gemini request
	var geminiReq struct {
		Contents []struct {
			Role  string         `json:"role"`
			Parts jsontext.Value `json:"parts"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(geminiBytes, &geminiReq); err != nil {
		t.Fatalf("unmarshal gemini request: %v", err)
	}

	// Find the assistant/model content
	var modelContent *struct {
		Role  string         `json:"role"`
		Parts jsontext.Value `json:"parts"`
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

func TestGeminiThoughtSignature(t *testing.T) {
	// Simulate the translation from OpenAI (derived from Anthropic tool_use) to Gemini
	openaiReq := `{
		"model": "gemini-3.5-flash",
		"messages": [
			{
				"role": "assistant",
				"content": "",
				"tool_calls": [
					{
						"id": "call_1__ts__test_signature",
						"type": "function",
						"function": {
							"name": "get_weather",
							"arguments": "{}"
						}
					}
				]
			}
		]
	}`

	geminiBytes, err := TranslateOpenAIToGemini([]byte(openaiReq))
	if err != nil {
		t.Fatalf("TranslateOpenAIToGemini failed: %v", err)
	}

	var geminiReq struct {
		Contents []struct {
			Parts []GeminiPart `json:"parts"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(geminiBytes, &geminiReq); err != nil {
		t.Fatalf("unmarshal gemini request: %v", err)
	}

	if len(geminiReq.Contents) == 0 {
		t.Fatal("expected contents")
	}

	var found bool
	for _, p := range geminiReq.Contents[0].Parts {
		if p.FunctionCall != nil && p.FunctionCall.Name == "get_weather" {
			found = true
			if p.ThoughtSignature != "test_signature" {
				t.Errorf("expected thought_signature to be 'test_signature', got: %q", p.ThoughtSignature)
			}
		}
	}
	if !found {
		t.Fatal("expected get_weather functionCall")
	}
}

func TestThoughtSignatureBackfill(t *testing.T) {
	// A function call with no __ts__ transport (made by a non-Gemini model, or
	// the client dropped the id) must still be signed with the default so
	// Gemini thinking models don't 400.
	openaiReq := `{
		"model": "gemini-3.5-flash",
		"messages": [
			{"role": "user", "content": "hi"},
			{"role": "assistant", "content": "", "tool_calls": [{"id": "call_plain_0", "type": "function", "function": {"name": "ScheduleWakeup", "arguments": "{\"time\":\"2026-08-13T00:00:00Z\"}"}}]}
		],
		"tools": [{"type": "function", "function": {"name": "ScheduleWakeup", "description": "wake", "parameters": {"type": "object", "properties": {"time": {"type": "string"}}}}}]
	}`

	geminiBytes, err := TranslateOpenAIToGemini([]byte(openaiReq))
	if err != nil {
		t.Fatalf("TranslateOpenAIToGemini failed: %v", err)
	}

	if !strings.Contains(string(geminiBytes), `"thoughtSignature":"`+DefaultThinkingSignature+`"`) {
		t.Errorf("unsigned functionCall must be backfilled with DefaultThinkingSignature, got: %s", geminiBytes)
	}
	if strings.Contains(string(geminiBytes), `"thought_signature"`) {
		t.Errorf("must emit camelCase thoughtSignature, got: %s", geminiBytes)
	}
}

func TestReasoningContentNotEmittedAsThoughtTrue(t *testing.T) {
	// When an assistant message has reasoning_content, it must NOT be emitted with "thought": true
	// in Gemini request history, because unsigned thought parts cause 400 "Corrupted thought signature".
	openaiReq := `{
		"model": "gemini-3.7-flash",
		"messages": [
			{"role": "user", "content": "hello"},
			{
				"role": "assistant",
				"content": "Here is the result",
				"reasoning_content": "I am thinking about hello...",
				"tool_calls": [
					{
						"id": "call_1__ts__sig123",
						"type": "function",
						"function": {"name": "test_tool", "arguments": "{}"}
					}
				]
			}
		]
	}`

	geminiBytes, err := TranslateOpenAIToGemini([]byte(openaiReq))
	if err != nil {
		t.Fatalf("TranslateOpenAIToGemini failed: %v", err)
	}

	if strings.Contains(string(geminiBytes), `"thought":true`) || strings.Contains(string(geminiBytes), `"thought": true`) {
		t.Errorf("history assistant messages must not contain 'thought: true' without signature, got: %s", geminiBytes)
	}
	if !strings.Contains(string(geminiBytes), "Here is the result") {
		t.Errorf("expected content to be preserved, got: %s", geminiBytes)
	}
}

func TestThoughtSignatureStreamMultiChunk(t *testing.T) {
	// Verify that when a thought_signature arrives in a thinking chunk (chunk 1),
	// a subsequent functionCall chunk (chunk 2) with no thought_signature inherits
	// the signature from state.LastThoughtSignature.
	state := &GeminiStreamState{}

	// Chunk 1: Thinking with thought_signature
	chunk1 := `{
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{
					"text": "Thinking about search...",
					"thought": true,
					"thought_signature": "SIG_FROM_THINKING_CHUNK"
				}]
			},
			"index": 0
		}]
	}`

	_, err := TranslateGeminiChunkToOpenAI([]byte(chunk1), state)
	if err != nil {
		t.Fatalf("chunk1 failed: %v", err)
	}
	if state.LastThoughtSignature != "SIG_FROM_THINKING_CHUNK" {
		t.Errorf("expected LastThoughtSignature to be saved, got %q", state.LastThoughtSignature)
	}

	// Chunk 2: FunctionCall without thought_signature
	chunk2 := `{
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{
					"functionCall": {
						"name": "search_tool",
						"args": {"query": "golang"}
					}
				}]
			},
			"index": 0
		}]
	}`

	openaiChunk, err := TranslateGeminiChunkToOpenAI([]byte(chunk2), state)
	if err != nil {
		t.Fatalf("chunk2 failed: %v", err)
	}
	if !strings.Contains(string(openaiChunk), "__ts__SIG_FROM_THINKING_CHUNK") {
		t.Errorf("functionCall in chunk2 must inherit thoughtSignature from chunk1, got: %s", string(openaiChunk))
	}
}

func TestTranslateGeminiResponseToOpenAI_PropagatesThoughtSig(t *testing.T) {
	// In non-stream, if thought_signature is on the thinking part, it should propagate to functionCall
	geminiResp := `{
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [
					{
						"text": "Let me think...",
						"thought": true,
						"thought_signature": "SIG_ON_THOUGHT_PART"
					},
					{
						"functionCall": {
							"name": "lookup",
							"args": {"id": 1}
						}
					}
				]
			},
			"finishReason": "STOP"
		}]
	}`

	openaiBytes, _, err := TranslateGeminiResponseToOpenAI([]byte(geminiResp))
	if err != nil {
		t.Fatalf("TranslateGeminiResponseToOpenAI failed: %v", err)
	}

	if !strings.Contains(string(openaiBytes), "__ts__SIG_ON_THOUGHT_PART") {
		t.Errorf("functionCall tool_call id must inherit thought_signature from thought part, got: %s", string(openaiBytes))
	}
}

func TestWrapForAntigravityPreservesThoughtSignature(t *testing.T) {
	geminiBody := []byte(`{"contents":[{"role":"model","parts":[{"thoughtSignature":"SIG123","functionCall":{"name":"get_weather","args":{}}}]}],"tools":[{"functionDeclarations":[{"name":"get_weather"}]}]}`)
	wrapped, err := WrapForAntigravity(geminiBody, "proj-1", "gemini-3.7-flash")
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	if !strings.Contains(string(wrapped), `"thoughtSignature":"SIG123"`) {
		t.Fatalf("expected thoughtSignature:SIG123 in wrapped output, got: %s", string(wrapped))
	}
}

