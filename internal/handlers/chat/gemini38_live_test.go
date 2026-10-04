package chat

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/proxy/executor"
)

// TestLiveE2E_Antigravity_Gemini38FlashMedium_RealChat hits the real upstream
// with gemini-3.8-flash-medium (no mocks). Skips gracefully when there is no
// real DB, no active connection, or the token is expired/rate-limited.
func TestLiveE2E_Antigravity_Gemini38FlashMedium_RealChat(t *testing.T) {
	repo, cleanup := getRealUserDB(t)
	defer cleanup()

	conns, err := repo.GetProviderConnections("antigravity", true)
	if err != nil || len(conns) == 0 {
		t.Skip("no active antigravity connections")
	}

	executor.RegisterAll()
	handler := NewChatHandler(repo)

	reqBody := `{
		"model": "ag/gemini-3.8-flash-medium",
		"messages": [
			{"role": "user", "content": "Say hello in one word"}
		],
		"max_tokens": 20,
		"stream": false
	}`

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(reqBody)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	t.Logf("Antigravity gemini-3.8-flash-medium response code: %d", rec.Code)
	t.Logf("Antigravity gemini-3.8-flash-medium response body: %s", rec.Body.String())

	requireLiveOK(t, rec.Code, rec.Body.String())
	if !strings.Contains(rec.Body.String(), "choices") {
		t.Errorf("expected choices in response, got: %s", rec.Body.String())
	}
}

// TestLiveE2E_Antigravity_Gemini38FlashMedium_RealStream is the streaming
// counterpart over the real upstream.
func TestLiveE2E_Antigravity_Gemini38FlashMedium_RealStream(t *testing.T) {
	repo, cleanup := getRealUserDB(t)
	defer cleanup()

	conns, err := repo.GetProviderConnections("antigravity", true)
	if err != nil || len(conns) == 0 {
		t.Skip("no active antigravity connections")
	}

	executor.RegisterAll()
	handler := NewChatHandler(repo)

	reqBody := `{
		"model": "ag/gemini-3.8-flash-medium",
		"messages": [
			{"role": "user", "content": "Count from 1 to 3 separated by commas"}
		],
		"max_tokens": 50,
		"stream": true
	}`

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(reqBody)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.HandleChatCompletions(rec, req)

	t.Logf("Antigravity gemini-3.8-flash-medium stream response code: %d", rec.Code)
	t.Logf("Antigravity gemini-3.8-flash-medium stream response body:\n%s", rec.Body.String())

	requireLiveSSE(t, rec.Code, rec.Body.String())
}
