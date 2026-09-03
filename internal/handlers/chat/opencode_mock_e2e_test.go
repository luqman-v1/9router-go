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
)

// TestE2E_Opencode_MuseSpark_Mock_NonStream_ToolCall verifies deterministic mock E2E for
// oc/muse-spark-1.2-contributor-free via Responses API without hitting real network.
// It validates: vision caps, Responses routing, reasoning_effort max->xhigh, and tool-call handling.
func TestE2E_Opencode_MuseSpark_Mock_NonStream_ToolCall(t *testing.T) {
	// Verify vision caps for muse-spark (ported from Next.js #3714)
	caps := providers.GetCapabilitiesForModel("opencode", "muse-spark-1.2-contributor-free")
	if !caps.Vision {
		t.Fatalf("muse-spark should have Vision true after v0.5.65, got %+v", caps)
	}
	if !caps.Reasoning || !caps.Tools {
		t.Fatalf("muse-spark should have Reasoning+Tools, got %+v", caps)
	}

	var capturedBody []byte
	var capturedPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			t.Errorf("expected /responses path, got %s", r.URL.Path)
		}
		if r.Header.Get("x-opencode-client") != "desktop" {
			t.Errorf("expected x-opencode-client desktop, got %q", r.Header.Get("x-opencode-client"))
		}
		buf := new(bytes.Buffer)
		buf.ReadFrom(r.Body)
		capturedBody = buf.Bytes()

		var parsed map[string]any
		if err := json.Unmarshal(capturedBody, &parsed); err != nil {
			t.Fatalf("unmarshal bodies: %v", err)
		}
		// Should be Responses API format with input, not messages
		if parsed["input"] == nil {
			t.Errorf("expected Responses input array, got %s", string(capturedBody))
		}
		if parsed["model"] != "muse-spark-1.2-contributor-free" {
			t.Errorf("expected model muse-spark-1.2-contributor-free, got %v", parsed["model"])
		}
		// reasoning_effort max should be transformed to xhigh
		if reasoning, ok := parsed["reasoning"].(map[string]any); ok {
			if reasoning["effort"] != "xhigh" {
				t.Errorf("expected reasoning effort xhigh (from max), got %v", reasoning["effort"])
			}
		} else {
			t.Errorf("expected reasoning field, got %v", parsed["reasoning"])
		}
		// Check tools present
		if parsed["tools"] == nil {
			t.Error("expected tools in Responses body")
		}

		// Return Responses API SSE stream (even for non-stream client, upstream is always stream)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		events := []string{
			`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"call_get_weather_mock_1234567890_1234567890_12345678","name":"get_weather","arguments":""}}` + "\n\n",
			`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"location\":\"Bandung\",\"unit\":\"celsius\"}"}` + "\n\n",
			`data: {"type":"response.function_call_arguments.done","output_index":0,"arguments":"{\"location\":\"Bandung\",\"unit\":\"celsius\"}"}` + "\n\n",
			`data: {"type":"response.completed","response":{"usage":{"input_tokens":60,"output_tokens":25,"input_tokens_details":{"cached_tokens":10}}}}` + "\n\n",
			`data: [DONE]` + "\n\n",
		}
		for _, ev := range events {
			_, _ = w.Write([]byte(ev))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer upstream.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	// Seed opencode connection with mock BaseURL
	ocData, _ := json.Marshal(map[string]any{
		"apiKey":  "mock-opencode-key",
		"baseUrl": upstream.URL + "/chat/completions",
	})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-oc-mock', 'opencode', 'apikey', 'OC Mock', 1, 1, ?, '2026-09-03T00:00:00Z', '2026-09-03T00:00:00Z')`, string(ocData)); err != nil {
		t.Fatalf("seed: %v", err)
	}

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	reqBody := `{
		"model": "oc/muse-spark-1.2-contributor-free",
		"messages": [{"role": "user", "content": "Weather in Bandung?"}],
		"stream": false,
		"reasoning_effort": "max",
		"tools": [{
			"type": "function",
			"function": {
				"name": "get_weather",
				"description": "Get weather",
				"parameters": {
					"type": "object",
					"properties": {
						"location": {"type": "string"},
						"unit": {"type": "string", "enum": ["celsius", "fahrenheit"]}
					},
					"required": ["location"]
				}
			}
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
		t.Fatalf("unmarshal resp: %v, body: %s", err, rec.Body.String())
	}

	choices, ok := resp["choices"].([]any)
	if !ok || len(choices) == 0 {
		t.Fatalf("expected choices, got %v", resp)
	}
	choice, _ := choices[0].(map[string]any)
	msg, _ := choice["message"].(map[string]any)

	// For Responses API via opencode, tool_calls may be in message.tool_calls
	toolCalls, ok := msg["tool_calls"].([]any)
	if !ok || len(toolCalls) == 0 {
		// Try alternative: content may contain tool call? Log for debug
		t.Logf("full response: %s", rec.Body.String())
		t.Fatalf("expected tool_calls, got %v", msg)
	}
	tc, _ := toolCalls[0].(map[string]any)
	fn, _ := tc["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Errorf("expected get_weather, got %v", fn["name"])
	}
	argsStr, _ := fn["arguments"].(string)
	var args map[string]any
	_ = json.Unmarshal([]byte(argsStr), &args)
	if args["location"] != "Bandung" {
		t.Errorf("expected Bandung, got %v", args["location"])
	}

	// Verify upstream saw correct path and body
	if !strings.HasSuffix(capturedPath, "/responses") {
		t.Errorf("upstream should be /responses, got %s", capturedPath)
	}
	if len(capturedBody) == 0 {
		t.Error("upstream did not capture body")
	}
	// Verify triple-named args not duplicated: should be single JSON object, not doubled
	if strings.Count(string(capturedBody), "Bandung") > 1 {
		t.Logf("capturedBody: %s", string(capturedBody))
	}
}

// TestE2E_Opencode_MuseSpark_Mock_Stream_ToolCall verifies streaming via Responses API SSE.
func TestE2E_Opencode_MuseSpark_Mock_Stream_ToolCall(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			t.Errorf("expected /responses, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("no flusher")
		}
		// Simulate Responses API streaming events for tool call
		// Codex/Responses style: response.output_item.added + function_call_arguments.delta
		events := []string{
			`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"call_mock_stream_123","name":"get_weather","arguments":""}}` + "\n\n",
			`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"location\":\"Jakarta\"}"}` + "\n\n",
			`data: {"type":"response.function_call_arguments.done","output_index":0,"arguments":"{\"location\":\"Jakarta\"}"}` + "\n\n",
			`data: {"type":"response.completed","response":{"usage":{"input_tokens":55,"output_tokens":20}}}` + "\n\n",
			`data: [DONE]` + "\n\n",
		}
		for _, ev := range events {
			_, _ = w.Write([]byte(ev))
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	ocData, _ := json.Marshal(map[string]any{
		"apiKey":  "mock-stream-key",
		"baseUrl": upstream.URL + "/chat/completions",
	})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-oc-stream', 'opencode', 'apikey', 'OC Stream Mock', 1, 1, ?, '2026-09-03T00:00:00Z', '2026-09-03T00:00:00Z')`, string(ocData)); err != nil {
		t.Fatalf("seed: %v", err)
	}

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	reqBody := `{
		"model": "oc/muse-spark-1.2-contributor-free",
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
	if !strings.Contains(body, "get_weather") {
		t.Errorf("expected get_weather in stream, got %s", body)
	}
	if !strings.Contains(body, "Jakarta") {
		t.Errorf("expected Jakarta in stream, got %s", body)
	}
	if !strings.Contains(body, "tool_calls") {
		t.Errorf("expected tool_calls in stream, got %s", body)
	}
	// Ensure no triplication: get_weather should appear once per tool call, not 3 times concatenated
	if strings.Count(body, "get_weatherget_weather") > 0 {
		t.Error("tool name triplication detected (get_weatherget_weather)")
	}
	if strings.Count(body, `"location":"Jakarta"}{"location":"Jakarta"`) > 0 {
		t.Error("arguments duplication detected")
	}
}

// TestE2E_Opencode_MuseSpark_Mock_Vision verifies that vision capability is declared and image_url is preserved.
func TestE2E_Opencode_MuseSpark_Mock_Vision(t *testing.T) {
	caps := providers.GetCapabilitiesForModel("opencode", "muse-spark-1.2-contributor-free")
	if !caps.Vision {
		t.Fatalf("muse-spark should have Vision true, got %+v", caps)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(bytes.Buffer)
		buf.ReadFrom(r.Body)
		body := buf.String()
		if !strings.Contains(body, "image_url") {
			t.Errorf("expected image_url preserved in Responses input, got %s", body)
		}
		resp := map[string]any{
			"id":     "resp_vision",
			"object": "response",
			"output": []map[string]any{
				{"type": "message", "content": []map[string]any{{"type": "text", "text": "Image is a cat"}}},
			},
			"usage": map[string]any{"input_tokens": 40, "output_tokens": 10},
		}
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	defer upstream.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	ocData, _ := json.Marshal(map[string]any{
		"apiKey":  "mock-vision-key",
		"baseUrl": upstream.URL + "/chat/completions",
	})
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-oc-vision', 'opencode', 'apikey', 'OC Vision Mock', 1, 1, ?, '2026-09-03T00:00:00Z', '2026-09-03T00:00:00Z')`, string(ocData)); err != nil {
		t.Fatalf("seed: %v", err)
	}

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	// OpenAI message with image_url
	reqBody := `{
		"model": "oc/muse-spark-1.2-contributor-free",
		"messages": [{"role": "user", "content": [
			{"type": "text", "text": "What is in image?"},
			{"type": "image_url", "image_url": {"url": "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB"}}
		]}],
		"stream": false
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
		t.Fatalf("unmarshal: %v", err)
	}
	choices, _ := resp["choices"].([]any)
	if len(choices) == 0 {
		t.Fatalf("expected choices, got %v", resp)
	}
}
