package translator

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"
	"testing"
)

func TestPreserveToolChoice(t *testing.T) {
	// OpenAI request with tool_choice: required
	openaiReq := `{
		"model": "gemini-3.5-flash",
		"messages": [{"role": "user", "content": "What is the weather?"}],
		"tools": [{
			"type": "function",
			"function": {"name": "get_weather", "parameters": {"type": "object"}}
		}],
		"tool_choice": "required"
	}`

	geminiBytes, err := TranslateOpenAIToGemini([]byte(openaiReq))
	if err != nil {
		t.Fatalf("TranslateOpenAIToGemini failed: %v", err)
	}

	body := string(geminiBytes)
	if !strings.Contains(body, "functionDeclarations") {
		t.Errorf("expected functionDeclarations in Gemini request, got: %s", body)
	}
	// tool_choice: required should translate to toolConfig mode ANY
	if !strings.Contains(body, `"mode":"ANY"`) {
		t.Errorf("expected toolConfig mode ANY with tool_choice=required, got: %s", body)
	}
}

func TestPreserveToolChoiceAuto(t *testing.T) {
	// OpenAI request with tool_choice: auto (default) - tools sent with VALIDATED
	openaiReq := `{
		"model": "gemini-3.5-flash",
		"messages": [{"role": "user", "content": "What is the weather?"}],
		"tools": [{
			"type": "function",
			"function": {"name": "get_weather", "parameters": {"type": "object"}}
		}],
		"tool_choice": "auto"
	}`

	geminiBytes, err := TranslateOpenAIToGemini([]byte(openaiReq))
	if err != nil {
		t.Fatalf("TranslateOpenAIToGemini failed: %v", err)
	}

	body := string(geminiBytes)
	if !strings.Contains(body, "functionDeclarations") {
		t.Errorf("expected functionDeclarations in Gemini request, got: %s", body)
	}
	// tool_choice: auto should translate to toolConfig mode VALIDATED
	if !strings.Contains(body, `"mode":"VALIDATED"`) {
		t.Errorf("expected toolConfig mode VALIDATED with tool_choice=auto, got: %s", body)
	}
}

func TestPreserveToolChoiceNone(t *testing.T) {
	// OpenAI request with tool_choice: none - tools should NOT be sent
	openaiReq := `{
		"model": "gemini-3.5-flash",
		"messages": [{"role": "user", "content": "Hello"}],
		"tools": [{
			"type": "function",
			"function": {"name": "get_weather", "parameters": {"type": "object"}}
		}],
		"tool_choice": "none"
	}`

	geminiBytes, err := TranslateOpenAIToGemini([]byte(openaiReq))
	if err != nil {
		t.Fatalf("TranslateOpenAIToGemini failed: %v", err)
	}

	body := string(geminiBytes)
	// With tool_choice: none, tools should NOT be sent, but toolConfig mode NONE
	// tells Gemini not to invoke any built-in capability
	if strings.Contains(body, "functionDeclarations") {
		t.Errorf("expected NO functionDeclarations with tool_choice=none, got: %s", body)
	}
	if !strings.Contains(body, `"mode":"NONE"`) {
		t.Errorf("expected toolConfig mode NONE with tool_choice=none, got: %s", body)
	}
}

func TestPreserveToolChoiceObjectDoesNotBreakUnmarshal(t *testing.T) {
	// OpenAI request with object-shaped tool_choice: {"type":"function","function":{"name":"get_weather"}}
	openaiReq := `{
		"model": "gemini-3.5-flash",
		"messages": [{"role": "user", "content": "Hello"}],
		"tools": [{
			"type": "function",
			"function": {"name": "get_weather", "parameters": {"type": "object"}}
		}],
		"tool_choice": {"type":"function","function":{"name":"get_weather"}}
	}`

	geminiBytes, err := TranslateOpenAIToGemini([]byte(openaiReq))
	if err != nil {
		t.Fatalf("TranslateOpenAIToGemini must not fail on object tool_choice: %v", err)
	}

	body := string(geminiBytes)
	if !strings.Contains(body, "functionDeclarations") {
		t.Errorf("expected functionDeclarations preserved with object tool_choice, got: %s", body)
	}
}

func TestPreserveFunctionResponseInOpenAIToGemini(t *testing.T) {
	// Multi-turn: user → assistant tool_call → tool result → final
	openaiReq := `{
		"model": "gemini-3.5-flash",
		"messages": [
			{"role": "user", "content": "What is the weather in Jakarta?"},
			{
				"role": "assistant",
				"content": "",
				"tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "get_weather", "arguments": "{\"location\":\"Jakarta\"}"}}]
			},
			{"role": "tool", "tool_call_id": "call_1", "content": "{\"temperature\": 32}"}
		],
		"tools": [{
			"type": "function",
			"function": {"name": "get_weather", "parameters": {"type": "object", "properties": {"location": {"type": "string"}}}}
		}]
	}`

	geminiBytes, err := TranslateOpenAIToGemini([]byte(openaiReq))
	if err != nil {
		t.Fatalf("TranslateOpenAIToGemini failed: %v", err)
	}

	body := string(geminiBytes)
	if !strings.Contains(body, "functionResponse") {
		t.Errorf("expected functionResponse in Gemini request for tool result, got: %s", body)
	}
	if !strings.Contains(body, "get_weather") {
		t.Errorf("expected get_weather in functionResponse, got: %s", body)
	}
}

func TestTranslateGeminiResponseFunctionCallPreserved(t *testing.T) {
	geminiResp := `{
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [
					{"text": "Calling weather tool..."},
					{"functionCall": {"name": "get_weather", "args": {"location": "Jakarta"}, "id": "call_1"}}
				]
			},
			"finishReason": "STOP"
		}],
		"usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 20}
	}`

	openaiBytes, _, err := TranslateGeminiResponseToOpenAI([]byte(geminiResp))
	if err != nil {
		t.Fatalf("TranslateGeminiResponseToOpenAI failed: %v", err)
	}

	body := string(openaiBytes)
	if !strings.Contains(body, "tool_calls") {
		t.Errorf("expected tool_calls in OpenAI response, got: %s", body)
	}
	if !strings.Contains(body, "get_weather") {
		t.Errorf("expected get_weather in tool_calls, got: %s", body)
	}
	if !strings.Contains(body, `"finish_reason":"tool_calls"`) {
		t.Errorf("expected finish_reason tool_calls, got: %s", body)
	}
}

func TestTranslateGeminiChunkFunctionCallPreserved(t *testing.T) {
	state := &GeminiStreamState{}

	geminiChunk := `{
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [
					{"functionCall": {"name": "search_web", "args": {"query": "test"}, "id": "call_search_1"}}
				]
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

	body := string(openaiChunk)
	if !strings.Contains(body, "tool_calls") {
		t.Errorf("expected tool_calls in stream chunk, got: %s", body)
	}
	if !strings.Contains(body, "search_web") {
		t.Errorf("expected search_web in tool_calls, got: %s", body)
	}
}

func TestTranslateOpenAIToGeminiToolsJsonTextValue(t *testing.T) {
	// Verify that jsontext.Value for Tools properly round-trips
	var oreq struct {
		Tools jsontext.Value `json:"tools,omitempty"`
	}
	toolsJSON := `[{"type":"function","function":{"name":"test_tool","parameters":{"type":"object"}}}]`
	if err := json.Unmarshal([]byte(`{"tools":`+toolsJSON+`}`), &oreq); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(oreq.Tools) == 0 {
		t.Error("oreq.Tools should not be empty")
	}
	if !strings.Contains(string(oreq.Tools), "test_tool") {
		t.Errorf("expected test_tool in Tools value, got: %s", string(oreq.Tools))
	}

	// Verify it can be unmarshaled back
	var openaiTools []OpenAITool
	if err := json.Unmarshal(oreq.Tools, &openaiTools); err != nil {
		t.Fatalf("unmarshal tools: %v", err)
	}
	if len(openaiTools) != 1 || openaiTools[0].Function.Name != "test_tool" {
		t.Errorf("expected 1 tool named test_tool, got %d tools", len(openaiTools))
	}
}
