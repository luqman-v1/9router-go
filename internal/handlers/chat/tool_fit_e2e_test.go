package chat

import (
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

func TestE2E_FitToolNames_OpenAI_NonStreaming(t *testing.T) {
	longToolName := "mcp__server_name__action_detail_something_long_tool_name_exceed_64_ab" // 68 chars
	var upstreamReceivedToolName string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}

		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}

		// Strictly enforce 64 characters rule as described in Issue #68
		tools, ok := req["tools"].([]any)
		if !ok || len(tools) == 0 {
			http.Error(w, "missing tools", http.StatusBadRequest)
			return
		}
		t0 := tools[0].(map[string]any)
		fn := t0["function"].(map[string]any)
		name := fn["name"].(string)

		if len(name) > 64 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":{"message":"Error from provider (Console): name must be at most 64 characters, got %d","type":"upstream_error"}}`, len(name))
			return
		}

		upstreamReceivedToolName = name

		// Upstream responds with tool call referencing the shortened name it saw
		resp := map[string]any{
			"id":      "chatcmpl-test-fit",
			"object":  "chat.completion",
			"created": 1234567890,
			"model":   "deepseek-chat",
			"choices": []any{
				map[string]any{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": nil,
						"tool_calls": []any{
							map[string]any{
								"id":   "call_mcp_123",
								"type": "function",
								"function": map[string]any{
									"name":      name,
									"arguments": `{"arg": "val"}`,
								},
							},
						},
					},
					"finish_reason": "tool_calls",
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.MarshalWrite(w, resp)
	}))
	defer upstream.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	_, _ = database.Exec("DELETE FROM providerConnections WHERE id IN ('conn-1', 'conn-2')")

	seedConnDB(t, database, "deepseek", "conn-fit-tools", "sk-test", upstream.URL)
	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	reqPayload := map[string]any{
		"model": "deepseek/deepseek-chat",
		"messages": []any{
			map[string]any{"role": "user", "content": "run mcp tool"},
		},
		"tools": []any{
			map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        longToolName,
					"description": "test tool exceeding 64 characters",
					"parameters": map[string]any{
						"type":       "object",
						"properties": map[string]any{"arg": map[string]any{"type": "string"}},
					},
				},
			},
		},
	}
	bodyBytes, _ := json.Marshal(reqPayload)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(bodyBytes)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify that upstream received fitted name <= 64 chars
	if len(upstreamReceivedToolName) > 64 {
		t.Errorf("upstream received name exceeding 64 chars: %d (%s)", len(upstreamReceivedToolName), upstreamReceivedToolName)
	}
	if !strings.HasSuffix(upstreamReceivedToolName, "_1") {
		t.Errorf("expected upstream to receive name ending with _1, got: %s", upstreamReceivedToolName)
	}

	// Verify that client received restored original long tool name
	var clientResp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &clientResp); err != nil {
		t.Fatalf("failed to parse client response: %v", err)
	}

	choices := clientResp["choices"].([]any)
	choice0 := choices[0].(map[string]any)
	msg := choice0["message"].(map[string]any)
	toolCalls := msg["tool_calls"].([]any)
	call0 := toolCalls[0].(map[string]any)
	callFn := call0["function"].(map[string]any)
	restoredName := callFn["name"].(string)

	if restoredName != longToolName {
		t.Errorf("expected restored name to be %q, got: %q", longToolName, restoredName)
	}
}

func TestE2E_FitToolNames_OpenAI_Streaming(t *testing.T) {
	longToolName := "mcp__server_name__action_detail_something_long_tool_name_exceed_64_ab" // 68 chars
	var upstreamReceivedToolName string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}

		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}

		tools, ok := req["tools"].([]any)
		if !ok || len(tools) == 0 {
			http.Error(w, "missing tools", http.StatusBadRequest)
			return
		}
		t0 := tools[0].(map[string]any)
		fn := t0["function"].(map[string]any)
		name := fn["name"].(string)

		if len(name) > 64 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":{"message":"name must be at most 64 characters, got %d"}}`, len(name))
			return
		}

		upstreamReceivedToolName = name

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("no flusher")
		}

		// Stream chunk 1: tool call start with name
		chunk1 := fmt.Sprintf(`data: {"id":"chatcmpl-fit-stream","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"%s","arguments":""}}]}}]}%s`, name, "\n\n")
		_, _ = w.Write([]byte(chunk1))
		flusher.Flush()

		// Stream chunk 2: arguments delta
		chunk2 := `data: {"id":"chatcmpl-fit-stream","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}\n\n`
		_, _ = w.Write([]byte(chunk2))
		flusher.Flush()

		// Stream chunk 3: finish_reason
		chunk3 := `data: {"id":"chatcmpl-fit-stream","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}\n\n`
		_, _ = w.Write([]byte(chunk3))
		flusher.Flush()

		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer upstream.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	_, _ = database.Exec("DELETE FROM providerConnections WHERE id IN ('conn-1', 'conn-2')")

	seedConnDB(t, database, "deepseek", "conn-fit-stream", "sk-test", upstream.URL)
	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	reqPayload := map[string]any{
		"model":  "deepseek/deepseek-chat",
		"stream": true,
		"messages": []any{
			map[string]any{"role": "user", "content": "run mcp stream"},
		},
		"tools": []any{
			map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": longToolName,
				},
			},
		},
	}
	bodyBytes, _ := json.Marshal(reqPayload)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(bodyBytes)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	bodyStr := rec.Body.String()
	if !strings.Contains(bodyStr, longToolName) {
		t.Errorf("expected stream to contain restored long tool name %q, got: %s", longToolName, bodyStr)
	}
	if strings.Contains(bodyStr, upstreamReceivedToolName) {
		t.Errorf("stream should not contain shortened name %q: %s", upstreamReceivedToolName, bodyStr)
	}
}

func TestE2E_FitToolNames_MultiTurn_Conversation(t *testing.T) {
	longToolName := "mcp__server_name__action_detail_something_long_tool_name_exceed_64_ab" // 68 chars
	var seenNamesInMessages []string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)

		// Check all messages for tool names
		if msgs, ok := req["messages"].([]any); ok {
			for _, m := range msgs {
				mm := m.(map[string]any)
				if tc, ok := mm["tool_calls"].([]any); ok {
					for _, c := range tc {
						fn := c.(map[string]any)["function"].(map[string]any)
						seenNamesInMessages = append(seenNamesInMessages, fn["name"].(string))
					}
				}
			}
		}

		resp := map[string]any{
			"id":      "chatcmpl-fit-turn2",
			"object":  "chat.completion",
			"created": 1234567890,
			"model":   "deepseek-chat",
			"choices": []any{
				map[string]any{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": "Action completed successfully!",
					},
					"finish_reason": "stop",
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.MarshalWrite(w, resp)
	}))
	defer upstream.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	_, _ = database.Exec("DELETE FROM providerConnections WHERE id IN ('conn-1', 'conn-2')")

	seedConnDB(t, database, "deepseek", "conn-fit-turn", "sk-test", upstream.URL)
	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	// Turn 2 request: includes history with assistant's previous tool_call with the long tool name
	reqPayload := map[string]any{
		"model": "deepseek/deepseek-chat",
		"messages": []any{
			map[string]any{"role": "user", "content": "run mcp tool"},
			map[string]any{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []any{
					map[string]any{
						"id":   "call_turn_1",
						"type": "function",
						"function": map[string]any{
							"name":      longToolName,
							"arguments": `{}`,
						},
					},
				},
			},
			map[string]any{
				"role":         "tool",
				"tool_call_id": "call_turn_1",
				"content":      `{"status": "ok"}`,
			},
			map[string]any{"role": "user", "content": "what happened?"},
		},
		"tools": []any{
			map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": longToolName,
				},
			},
		},
	}
	bodyBytes, _ := json.Marshal(reqPayload)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(bodyBytes)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	if len(seenNamesInMessages) == 0 {
		t.Fatal("expected upstream to see assistant tool call in messages")
	}
	for _, name := range seenNamesInMessages {
		if len(name) > 64 {
			t.Errorf("assistant tool call in message was not fitted: len %d (%s)", len(name), name)
		}
		if !strings.HasSuffix(name, "_1") {
			t.Errorf("expected fitted name to have _1 suffix, got: %s", name)
		}
	}
}
