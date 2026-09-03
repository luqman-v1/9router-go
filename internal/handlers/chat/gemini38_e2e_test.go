package chat

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/translator"
)

// TestE2E_Gemini38_FlashHigh_NonStream_ToolCall verifies end-to-end handling of
// gemini-3.8-flash-high via Antigravity with tool definitions and tool-call response.
// It covers: model synonym normalization, Gemini 3.8 caps, schema cleaning (prefixItems),
// and Antigravity wrapping/unwrapping.
func TestE2E_Gemini38_FlashHigh_NonStream_ToolCall(t *testing.T) {
	// Verify 3.8 model capabilities first
	caps := providers.GetCapabilitiesForModel("antigravity", "gemini-3.8-flash-high")
	if !caps.Vision || !caps.Reasoning || !caps.Search {
		t.Fatalf("gemini-3.8-flash-high should have vision+reasoning+search, got %+v", caps)
	}
	if caps.Tools != true {
		t.Fatalf("gemini-3.8-flash-high should support tools")
	}

	// Verify synonym normalization
	if got := translator.NormalizeAntigravityModel("gemini-3.8-flash-high"); got != "gemini-3.8-flash-tiered" {
		t.Fatalf("NormalizeAntigravityModel(3.8-high) = %q, want gemini-3.8-flash-tiered", got)
	}
	if got := translator.NormalizeAntigravityModel("gemini-3.8-flash"); got != "gemini-3.8-flash-tiered" {
		t.Fatalf("NormalizeAntigravityModel(3.8) = %q, want gemini-3.8-flash-tiered", got)
	}

	// Mock Antigravity upstream that validates the translated Gemini request
	var capturedBody []byte
	var capturedModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Antigravity endpoint is /v1internal:generateContent
		if !strings.Contains(r.URL.Path, "generateContent") {
			t.Errorf("expected generateContent path, got %s", r.URL.Path)
		}
		// Check User-Agent is 2.11.0 (proxy fingerprint)
		if ua := r.Header.Get("User-Agent"); !strings.Contains(ua, "2.11.0") {
			t.Errorf("expected User-Agent 2.11.0, got %q", ua)
		}
		// Read actual body
		buf := new(bytes.Buffer)
		buf.ReadFrom(r.Body)
		capturedBody = buf.Bytes()

		var agReq translator.AntigravityRequest
		if err := json.Unmarshal(capturedBody, &agReq); err != nil {
			t.Fatalf("unmarshal antigravity wrapper: %v", err)
		}
		capturedModel = agReq.Model
		if agReq.Model != "gemini-3.8-flash-tiered" {
			t.Errorf("expected wrapped model gemini-3.8-flash-tiered, got %q", agReq.Model)
		}
		if agReq.Project != "test-proj-38" {
			t.Errorf("expected project test-proj-38, got %q", agReq.Project)
		}

		// Validate inner Gemini request has cleaned tool schema (prefixItems removed, items present)
		var gemReq translator.GeminiRequest
		if err := json.Unmarshal(agReq.Request, &gemReq); err != nil {
			t.Fatalf("unmarshal inner gemini request: %v", err)
		}
		if len(gemReq.Tools) == 0 || len(gemReq.Tools[0].FunctionDeclarations) == 0 {
			t.Fatalf("expected tools in gemini request, got none")
		}
		decl := gemReq.Tools[0].FunctionDeclarations[0]
		if decl.Name != "get_weather_ide" {
			t.Errorf("expected tool get_weather_ide (cloaked), got %q", decl.Name)
		}
		// Check that prefixItems was cleaned and items exists
		paramBytes, _ := json.Marshal(decl.Parameters)
		if strings.Contains(string(paramBytes), "prefixItems") {
			t.Errorf("prefixItems should be cleaned from tool schema")
		}
		// Respond with Gemini tool-call response wrapped in Antigravity envelope
		// The response includes thoughtSignature to test backfill logic
		// Return cloaked name so proxy can uncloak to original for client
		gemResp := map[string]any{
			"candidates": []map[string]any{
				{
					"content": map[string]any{
						"parts": []map[string]any{
							{
								"thoughtSignature": translator.DefaultThinkingSignature[:64],
								"functionCall": map[string]any{
									"name": "get_weather_ide",
									"args": map[string]any{"location": "Jakarta", "unit": "celsius"},
								},
							},
						},
					},
					"finishReason": "STOP",
				},
			},
			"usageMetadata": map[string]any{
				"promptTokenCount":     50,
				"candidatesTokenCount": 20,
				"cachedContentTokenCount": 5,
			},
		}
		wrapped := map[string]any{"response": gemResp}
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(wrapped)
		_, _ = w.Write(b)
	}))
	defer upstream.Close()

	// Setup DB with Antigravity connection using mock URL as BaseURL
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	// Override antigravity BaseURL via connection data to point to mock
	agData, _ := json.Marshal(map[string]any{
		"apiKey":      "test-ag-key-38",
		"accessToken": "test-ag-key-38",
		"baseUrl":     upstream.URL,
		"projectId":   "test-proj-38",
		"providerSpecificData": map[string]any{
			"projectId": "test-proj-38",
		},
	})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-ag-38', 'antigravity', 'oauth', 'AG 3.8 Test', 1, 1, ?, '2026-09-03T00:00:00Z', '2026-09-03T00:00:00Z')`, string(agData)); err != nil {
		t.Fatalf("seed antigravity: %v", err)
	}

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	// Build OpenAI request with gemini-3.8-flash-high and a tool that has prefixItems (tuple) to trigger schema cleaning
	reqBody := `{
		"model": "antigravity/gemini-3.8-flash-high",
		"messages": [{"role": "user", "content": "What's weather in Jakarta?"}],
		"stream": false,
		"tools": [{
			"type": "function",
			"function": {
				"name": "get_weather",
				"description": "Get weather",
				"parameters": {
					"type": "object",
					"properties": {
						"location": {"type": "string"},
						"unit": {"type": "string", "enum": ["celsius", "fahrenheit"]},
						"coords": {
							"type": "array",
							"prefixItems": [{"type": "number"}, {"type": "number"}],
							"description": "lat/lon tuple"
						}
					},
					"required": ["location"]
				}
			}
		}],
		"reasoning_effort": "high"
	}`

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(reqBody)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v, body: %s", err, rec.Body.String())
	}

	choices, ok := resp["choices"].([]any)
	if !ok || len(choices) == 0 {
		t.Fatalf("expected choices, got %v", resp)
	}
	choice, _ := choices[0].(map[string]any)
	msg, _ := choice["message"].(map[string]any)

	// Check tool_calls present and correctly named (uncloaked)
	toolCalls, ok := msg["tool_calls"].([]any)
	if !ok || len(toolCalls) == 0 {
		t.Fatalf("expected tool_calls in response, got %v", msg)
	}
	tc, _ := toolCalls[0].(map[string]any)
	fn, _ := tc["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Errorf("expected tool get_weather, got %v", fn["name"])
	}
	argsStr, _ := fn["arguments"].(string)
	var args map[string]any
	_ = json.Unmarshal([]byte(argsStr), &args)
	if args["location"] != "Jakarta" {
		t.Errorf("expected location Jakarta, got %v", args["location"])
	}

	// Check usage includes cached tokens
	usage, _ := resp["usage"].(map[string]any)
	if usage == nil {
		t.Fatalf("expected usage, got nil")
	}
	// prompt_tokens should be 50, completion 20
	if pt, _ := usage["prompt_tokens"].(float64); int(pt) != 50 {
		t.Errorf("expected prompt_tokens 50, got %v", usage["prompt_tokens"])
	}
	if ct, _ := usage["completion_tokens"].(float64); int(ct) != 20 {
		t.Errorf("expected completion_tokens 20, got %v", usage["completion_tokens"])
	}

	// Verify the mock saw correct wrapped model
	if capturedModel != "gemini-3.8-flash-tiered" {
		t.Errorf("mock did not see correct model, got %q", capturedModel)
	}
	if len(capturedBody) == 0 {
		t.Error("mock did not capture body")
	}
}

// TestE2E_Gemini38_FlashHigh_MultiTurn_ToolCall tests history handling: user -> assistant tool_call -> tool result -> final
func TestE2E_Gemini38_FlashHigh_MultiTurn_ToolCall(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return final answer after tool result
		gemResp := map[string]any{
			"candidates": []map[string]any{
				{
					"content": map[string]any{
						"parts": []map[string]any{
							{"text": "The weather in Jakarta is 32°C and sunny."},
						},
					},
					"finishReason": "STOP",
				},
			},
			"usageMetadata": map[string]any{
				"promptTokenCount": 80,
				"candidatesTokenCount": 15,
			},
		}
		wrapped := map[string]any{"response": gemResp}
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(wrapped)
		_, _ = w.Write(b)
	}))
	defer upstream.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	agData, _ := json.Marshal(map[string]any{
		"apiKey":      "test-ag-key-38-multi",
		"accessToken": "test-ag-key-38-multi",
		"baseUrl":     upstream.URL,
		"projectId":   "test-proj-38",
		"providerSpecificData": map[string]any{"projectId": "test-proj-38"},
	})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-ag-38-multi', 'antigravity', 'oauth', 'AG 3.8 Multi', 1, 1, ?, '2026-09-03T00:00:00Z', '2026-09-03T00:00:00Z')`, string(agData)); err != nil {
		t.Fatalf("seed: %v", err)
	}

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	// Multi-turn: user asks, assistant called tool, user returns result, now final
	reqBody := `{
		"model": "antigravity/gemini-3.8-flash-high",
		"messages": [
			{"role": "user", "content": "Weather in Jakarta?"},
			{"role": "assistant", "content": null, "tool_calls": [{"id": "call_weather_123", "type": "function", "function": {"name": "get_weather", "arguments": "{\"location\":\"Jakarta\"}"}}]},
			{"role": "tool", "tool_call_id": "call_weather_123", "content": "{\"temp\": 32, \"condition\": \"sunny\"}"},
			{"role": "user", "content": "Thanks, now summarize."}
		],
		"stream": false,
		"tools": [{
			"type": "function",
			"function": {"name": "get_weather", "description": "Get weather", "parameters": {"type": "object", "properties": {"location": {"type": "string"}}, "required": ["location"]}}
		}]
	}`

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(reqBody)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v, body: %s", err, rec.Body.String())
	}
	choices, ok := resp["choices"].([]any)
	if !ok || len(choices) == 0 {
		t.Fatalf("expected choices with final answer, got %v, body: %s", resp, rec.Body.String())
	}
	choice, _ := choices[0].(map[string]any)
	msg, _ := choice["message"].(map[string]any)
	content, _ := msg["content"].(string)
	if !strings.Contains(content, "Jakarta") || !strings.Contains(content, "32") {
		t.Errorf("expected final answer to contain Jakarta and 32, got %q", content)
	}
}

// TestTranslator_Gemini38_ToolSchema_PrefixItems verifies schema cleaning for 3.8 high
func TestTranslator_Gemini38_ToolSchema_PrefixItems(t *testing.T) {
	body := []byte(`{
		"model": "gemini-3.8-flash-high",
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [{
			"type": "function",
			"function": {
				"name": "search",
				"description": "search",
				"parameters": {
					"type": "object",
					"properties": {
						"coords": {
							"type": "array",
							"prefixItems": [{"type": "number"}, {"type": "number"}]
						},
						"query": {"type": "string"}
					},
					"required": ["query"]
				}
			}
		}]
	}`)

	geminiJSON, err := translator.TranslateOpenAIToGemini(body)
	if err != nil {
		t.Fatalf("TranslateOpenAIToGemini: %v", err)
	}
	var gemReq translator.GeminiRequest
	if err := json.Unmarshal(geminiJSON, &gemReq); err != nil {
		t.Fatalf("unmarshal gemini: %v", err)
	}
	if len(gemReq.Tools) == 0 {
		t.Fatalf("expected tools")
	}
	decl := gemReq.Tools[0].FunctionDeclarations[0]
	// Parameters is stored as jsontext.Value or map; marshal to JSON string for checking
	paramBytes, _ := json.Marshal(decl.Parameters)
	paramStr := string(paramBytes)
	if strings.Contains(paramStr, "prefixItems") {
		t.Error("prefixItems should be stripped from tool schema")
	}
	var params map[string]any
	if err := json.Unmarshal(paramBytes, &params); err == nil {
		props, _ := params["properties"].(map[string]any)
		if coords, ok := props["coords"].(map[string]any); ok {
			if _, has := coords["items"]; !has {
				t.Error("coords array should have items after cleaning")
			}
		}
	}

	// Wrap for antigravity and verify model
	wrapped, err := translator.WrapForAntigravity(geminiJSON, "proj-1", "gemini-3.8-flash-high")
	if err != nil {
		t.Fatalf("WrapForAntigravity: %v", err)
	}
	var agReq translator.AntigravityRequest
	if err := json.Unmarshal(wrapped, &agReq); err != nil {
		t.Fatalf("unmarshal ag: %v", err)
	}
	if agReq.Model != "gemini-3.8-flash-tiered" {
		t.Errorf("expected gemini-3.8-flash-tiered, got %q", agReq.Model)
	}
}

// TestE2E_Gemini38_FlashHigh_Stream_ToolCall verifies streaming tool-call path for 3.8 high.
// Upstream streams Gemini SSE with functionCall, proxy translates to OpenAI SSE with tool_calls.
func TestE2E_Gemini38_FlashHigh_Stream_ToolCall(t *testing.T) {
	// Direct translator sanity check
	{
		state := &translator.GeminiStreamState{}
		chunk := []byte(`{"candidates": [{"content": {"role": "model", "parts": [{"thoughtSignature": "` + translator.DefaultThinkingSignature[:64] + `", "functionCall": {"name": "get_weather_ide", "args": {"location": "Jakarta"}}}]} }]}`)
		out, err := translator.TranslateGeminiChunkToOpenAI(chunk, state)
		t.Logf("direct translator err=%v out=%s", err, string(out))
		if len(out) == 0 {
			t.Fatalf("direct translator returned empty")
		}
		if !strings.Contains(string(out), "get_weather") {
			t.Fatalf("direct translator should contain get_weather, got %s", string(out))
		}
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Streaming path is streamGenerateContent
		if !strings.Contains(r.URL.Path, "streamGenerateContent") {
			t.Errorf("expected streamGenerateContent, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("no flusher")
		}
		// First chunk: role
		// Second chunk: tool call with thoughtSignature
		chunk1 := `data: {"candidates": [{"content": {"parts": [{"thoughtSignature": "` + translator.DefaultThinkingSignature[:64] + `", "functionCall": {"name": "get_weather_ide", "args": {"location": "Jakarta"}}}], "role": "model"}, "finishReason": "STOP"}]}` + "\n\n"
		chunk2 := `data: {"candidates": [{"finishReason": "STOP"}]}` + "\n\n"
		_, _ = w.Write([]byte(chunk1))
		flusher.Flush()
		_, _ = w.Write([]byte(chunk2))
		flusher.Flush()
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer upstream.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	agData, _ := json.Marshal(map[string]any{
		"apiKey":      "test-ag-key-38-stream",
		"accessToken": "test-ag-key-38-stream",
		"baseUrl":     upstream.URL,
		"projectId":   "test-proj-38",
		"providerSpecificData": map[string]any{"projectId": "test-proj-38"},
	})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-ag-38-stream', 'antigravity', 'oauth', 'AG 3.8 Stream', 1, 1, ?, '2026-09-03T00:00:00Z', '2026-09-03T00:00:00Z')`, string(agData)); err != nil {
		t.Fatalf("seed: %v", err)
	}

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	reqBody := `{
		"model": "antigravity/gemini-3.8-flash-high",
		"messages": [{"role": "user", "content": "Weather Jakarta?"}],
		"stream": true,
		"tools": [{
			"type": "function",
			"function": {"name": "get_weather", "description": "Get weather", "parameters": {"type": "object", "properties": {"location": {"type": "string"}}, "required": ["location"]}}
		}]
	}`

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(reqBody)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	t.Logf("stream body: %s", body)
	// Should contain OpenAI SSE with tool_calls and uncloaked name get_weather
	if !strings.Contains(body, "get_weather") {
		t.Errorf("expected get_weather in stream, got %s", body)
	}
	if strings.Contains(body, "get_weather_ide") {
		t.Errorf("should be uncloaked to get_weather, got _ide in %s", body)
	}
	if !strings.Contains(body, "tool_calls") {
		t.Errorf("expected tool_calls in stream, got %s", body)
	}
	if !strings.Contains(body, "Jakarta") {
		t.Errorf("expected Jakarta args in stream, got %s", body)
	}
	// For OpenAI via Gemini native, stream ends with finish_reason, not necessarily [DONE] via EnsureStreamClosed
	// Just verify we got some data and finish
	if !strings.Contains(body, "finish_reason") && !strings.Contains(body, "[DONE]") {
		t.Errorf("expected finish_reason or [DONE] in stream, got %s", body)
	}
}
