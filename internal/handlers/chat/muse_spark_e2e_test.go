package chat

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/proxy/executor"
)

// upstreamUnavailable reports whether a free-tier status from the real
// opencode endpoint means the provider is unavailable rather than the gateway
// being wrong: a rate limit (429), a free-tier refusal (403), or a backend
// overload (503). These tests call the live endpoint, so any of the three can
// arrive at any moment and none of them is a defect here.
//
// 503 was the one still missing: muse-spark-1.3 returned "Error from provider
// (Console): The backend is temporarily overloaded" and failed CI on #82 while
// the four sibling tests already skipped on 429/403. A test whose outcome
// depends on a third party's availability has to skip on every status that
// means "not right now", or CI fails at the mercy of the provider's load.
func upstreamUnavailable(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusForbidden, http.StatusServiceUnavailable:
		return true
	default:
		return false
	}
}


func TestIntegration_OpenCode_MuseSpark_Messages(t *testing.T) {
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

	if upstreamUnavailable(rec.Code) {
		t.Skipf("opencode free tier unavailable (%d), skipping real upstream test: %s", rec.Code, rec.Body.String())
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}
	bodyStr := rec.Body.String()
	if !strings.Contains(bodyStr, "event: message_start") || (!strings.Contains(bodyStr, "event: content_block_delta") && !strings.Contains(bodyStr, "event: message_delta")) {
		t.Fatalf("expected Claude SSE format (message_start / message_delta), got: %s", bodyStr)
	}
}

func TestIntegration_OpenCode_MuseSpark_Messages_NonStreaming(t *testing.T) {
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

	if upstreamUnavailable(rec.Code) {
		t.Skipf("opencode free tier unavailable (%d), skipping: %s", rec.Code, rec.Body.String())
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}
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

	if upstreamUnavailable(rec.Code) {
		t.Skipf("opencode free tier unavailable (%d), skipping: %s", rec.Code, rec.Body.String())
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestIntegration_OpenCode_MuseSpark_MultiTurnWithTools(t *testing.T) {
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

	if upstreamUnavailable(rec.Code) {
		t.Skipf("opencode free tier unavailable (%d), skipping: %s", rec.Code, rec.Body.String())
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestIntegration_OpenCode_MuseSpark13_ChatCompletions(t *testing.T) {
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

	if upstreamUnavailable(rec.Code) {
		t.Skipf("opencode free tier unavailable (%d), skipping: %s", rec.Code, rec.Body.String())
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200 for muse-spark-1.3, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "FreeTierError") {
		t.Fatalf("unexpected FreeTierError: %s", rec.Body.String())
	}
}

// Pins the skip set itself, so the next "not right now" status the free tier
// starts returning is a one-line change in upstreamUnavailable rather than
// another red CI run discovered after the fact. A real defect status must stay
// outside it: 400/500 mean the gateway got something wrong and the test should
// still fail loudly.
func TestUpstreamUnavailable_SkipsOnlyProviderAvailabilityStatuses(t *testing.T) {
	tests := []struct {
		name string
		code int
		want bool
	}{
		{name: "rate limited", code: http.StatusTooManyRequests, want: true},
		{name: "free tier refused", code: http.StatusForbidden, want: true},
		{name: "backend overloaded", code: http.StatusServiceUnavailable, want: true},
		{name: "success", code: http.StatusOK, want: false},
		{name: "bad request is our bug", code: http.StatusBadRequest, want: false},
		{name: "server error is our bug", code: http.StatusInternalServerError, want: false},
		{name: "auth failure is a real credential problem", code: http.StatusUnauthorized, want: false},
		{name: "bad gateway is the upstream path", code: http.StatusBadGateway, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := upstreamUnavailable(tt.code); got != tt.want {
				t.Errorf("upstreamUnavailable(%d) = %v, want %v", tt.code, got, tt.want)
			}
		})
	}
}
