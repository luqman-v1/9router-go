package chat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

func TestHandleMessagesCombo_ClaudeStreamingPassthrough(t *testing.T) {
	// A Claude Messages SSE stream from an upstream Claude-native member
	// must pass through without attempting OpenAI->Claude translation,
	// which would yield zero chunks and trigger ErrStreamingZeroContent.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"minimax-m3.1-flash\",\"content\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello world\"}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"))
		_, _ = w.Write([]byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n"))
		_, _ = w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer upstream.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	if _, err := database.Exec(`INSERT INTO providerConnections
		(id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		VALUES ('conn-anth-stream-combo', 'anthropic-compatible-acme', 'apikey', 'acme1', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		`{"apiKey":"sk-test","providerSpecificData":{"baseUrl":"`+upstream.URL+`/proxy/v1"}}`); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	handler := NewChatHandler(db.NewRepo(database))
	translatedReq := map[string]any{
		"model":      "minimax-m3.1-flash",
		"max_tokens": 100,
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
	}
	comboModels := []string{"anthropic-compatible-acme/minimax-m3.1-flash"}
	rec := httptest.NewRecorder()
	handler.handleMessagesComboFallback(context.Background(), rec, translatedReq, comboModels, "fallback", true, "combo-anth", 0)

	body := rec.Body.String()
	if !strings.Contains(body, "Hello world") {
		t.Fatalf("expected Claude SSE stream passed through, got: %q", body)
	}
}
