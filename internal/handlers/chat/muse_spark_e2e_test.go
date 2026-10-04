package chat

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/proxy/executor"
)

// Every test in this file reaches the real opencode free tier over the network —
// setupChatTestDB seeds no opencode connection, so the model resolves straight
// to the provider. See live_guard_test.go for the opt-in, and run them with
// make test-live. (opencode_mock_e2e_test.go is the offline counterpart: it seeds
// its own conn-oc-* rows pointing at a local fake server.)

func TestIntegration_OpenCode_MuseSpark_Messages(t *testing.T) {
	requireLiveUpstream(t)
	executor.RegisterAll()
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	// Test Claude messages format: POST /v1/messages
	claudeBody := `{
		"model": "oc/muse-spark-1.2-contributor-free",
		"messages": [
			{"role": "user", "content": "Say hello in one word"}
		],
		"max_tokens": 1024,
		"stream": true,
		"system": "You are a concise assistant.",
		"tools": [
			{
				"name": "calc",
				"description": "Calculate expression",
				"input_schema": {
					"type": "object",
					"properties": {
						"expr": {"type": "string"}
					},
					"required": ["expr"]
				}
			}
		]
	}`

	req := httptest.NewRequest("POST", "/v1/messages", bytes.NewReader([]byte(claudeBody)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleMessages(rec, req)

	t.Logf("Response Code: %d", rec.Code)
	t.Logf("Response Body: %s", rec.Body.String())

	requireLiveOK(t, rec.Code, rec.Body.String())
	bodyStr := rec.Body.String()
	if !strings.Contains(bodyStr, "event: message_start") || (!strings.Contains(bodyStr, "event: content_block_delta") && !strings.Contains(bodyStr, "event: message_delta")) {
		t.Fatalf("expected Claude SSE format (message_start / message_delta), got: %s", bodyStr)
	}
}


func TestIntegration_OpenCode_MuseSpark_Messages_NonStreaming(t *testing.T) {
	requireLiveUpstream(t)
	executor.RegisterAll()
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	// Test Claude messages format: POST /v1/messages with stream=false
	claudeBody := `{
		"model": "oc/muse-spark-1.2-contributor-free",
		"messages": [
			{"role": "user", "content": "Say hello in one word"}
		],
		"max_tokens": 1024,
		"stream": false,
		"system": "You are a concise assistant."
	}`

	req := httptest.NewRequest("POST", "/v1/messages", bytes.NewReader([]byte(claudeBody)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleMessages(rec, req)

	t.Logf("Non-streaming Response Code: %d", rec.Code)
	t.Logf("Non-streaming Response Body: %s", rec.Body.String())

	requireLiveOK(t, rec.Code, rec.Body.String())
	contentType := rec.Header().Get("Content-Type")
	if !strings.Contains(contentType, "application/json") {
		t.Fatalf("expected application/json content type, got: %s", contentType)
	}
	bodyStr := rec.Body.String()
	if !strings.Contains(bodyStr, `"type":"message"`) || !strings.Contains(bodyStr, `"role":"assistant"`) {
		t.Fatalf("expected Claude JSON message structure, got: %s", bodyStr)
	}
}

func TestIntegration_OpenCode_MuseSpark_ChatCompletions(t *testing.T) {
	requireLiveUpstream(t)
	executor.RegisterAll()
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	// Test OpenAI chat format: POST /v1/chat/completions
	chatBody := `{
		"model": "oc/muse-spark-1.2-contributor-free",
		"messages": [
			{"role": "system", "content": "You are a concise assistant."},
			{"role": "user", "content": "Say hello in one word"}
		],
		"max_tokens": 1024,
		"stream": true,
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "calc",
					"description": "Calculate expression",
					"parameters": {
						"type": "object",
						"properties": {
							"expr": {"type": "string"}
						},
						"required": ["expr"]
					}
				}
			}
		]
	}`

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(chatBody)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	t.Logf("Response Code: %d", rec.Code)
	t.Logf("Response Body: %s", rec.Body.String())

	requireLiveOK(t, rec.Code, rec.Body.String())
}

func TestIntegration_OpenCode_MuseSpark_MultiTurnWithTools(t *testing.T) {
	requireLiveUpstream(t)
	executor.RegisterAll()
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	// Test multi-turn with tool calls and tool output
	claudeBody := `{
		"model": "oc/muse-spark-1.2-contributor-free",
		"messages": [
			{"role": "user", "content": "What is 2+2?"},
			{
				"role": "assistant",
				"content": [
					{"type": "text", "text": "I will calculate that for you."},
					{
						"type": "tool_use",
						"id": "toolu_01abcdefghijklmnopqrstuvwxyz_1234567890_abcdefghijklmnopqrstuvwxyz_1234567890_extra_long_identifier",
						"name": "calc",
						"input": {"expr": "2+2"}
					}
				]
			},
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "toolu_01abcdefghijklmnopqrstuvwxyz_1234567890_abcdefghijklmnopqrstuvwxyz_1234567890_extra_long_identifier",
						"content": "4"
					}
				]
			}
		],
		"max_tokens": 1024,
		"stream": true,
		"system": "You are a calculator assistant.",
		"tools": [
			{
				"name": "calc",
				"description": "Calculate expression",
				"input_schema": {
					"type": "object",
					"properties": {
						"expr": {"type": "string"}
					},
					"required": ["expr"]
				}
			}
		]
	}`

	req := httptest.NewRequest("POST", "/v1/messages", bytes.NewReader([]byte(claudeBody)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleMessages(rec, req)

	t.Logf("Response Code: %d", rec.Code)
	t.Logf("Response Body: %s", rec.Body.String())

	requireLiveOK(t, rec.Code, rec.Body.String())
}

func TestIntegration_OpenCode_MuseSpark13_ChatCompletions(t *testing.T) {
	requireLiveUpstream(t)
	executor.RegisterAll()
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	chatBody := `{
		"model": "oc/muse-spark-1.3-contributor-free",
		"messages": [
			{"role": "user", "content": "Say hello in one word"}
		],
		"stream": false
	}`

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(chatBody)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	t.Logf("1.3 Response Code: %d", rec.Code)
	t.Logf("1.3 Response Body: %s", rec.Body.String())

	requireLiveOK(t, rec.Code, rec.Body.String())
	if strings.Contains(rec.Body.String(), "FreeTierError") {
		t.Fatalf("unexpected FreeTierError: %s", rec.Body.String())
	}
}

