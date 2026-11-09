package chat

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandleChatCompletions_ComboStreamingZeroContentFailover tests that when a streaming
// upstream returns HTTP 200 with an event stream that has NO usable content (only role and empty finish_reason),
// the combo peeker detects it and falls through to the next candidate model in the combo.
func TestHandleChatCompletions_ComboStreamingZeroContentFailover(t *testing.T) {
	// Upstream A: returns 200 text/event-stream but only sends delta role and empty [DONE], no content tokens.
	emptySSEUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer emptySSEUpstream.Close()

	// Upstream B: returns 200 text/event-stream with real content.
	healthySSEUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		if req["model"] != "qwen3-32b" {
			t.Errorf("expected failover to groq/qwen3-32b, got model %v", req["model"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello from model B\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer healthySSEUpstream.Close()

	repo := seedComboWithTwoUpstreams(t, "combo-stream-zero", emptySSEUpstream.URL, healthySSEUpstream.URL, "stream-zero-test")

	rec := httptest.NewRecorder()
	handler := NewChatHandler(repo)
	req := httptest.NewRequest("POST", "/chat/completions",
		strings.NewReader(`{"model":"stream-zero-test","messages":[{"role":"user","content":"hi"}],"stream":true}`))

	handler.HandleChatCompletions(rec, req)

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("expected text/event-stream, got %q: %s", ct, rec.Body.String())
	}

	body := rec.Body.String()
	if !strings.Contains(body, "hello from model B") {
		t.Fatalf("expected response from model B, got body:\n%s", body)
	}
}
