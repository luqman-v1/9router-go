package chat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestHandleStreamResponse_RefusesEmptyTurnBeforeCommit is the point of the
// peek: once the gateway writes `200 text/event-stream` for a turn that never
// carried an answer, the combo and the account cooldown have both settled on a
// model that said nothing. Refusing before the header write keeps the failure
// reversible, so the fallback loop (which retries 502) runs normally.
func TestHandleStreamResponse_RefusesEmptyTurnBeforeCommit(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	cases := []struct {
		name   string
		stream string
	}{
		{name: "immediate EOF", stream: ""},
		{name: "keep-alive comments only", stream: ": keep-alive\n\n: keep-alive\n\n"},
		{name: "DONE with no token", stream: "data: [DONE]\n\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			err := h.handleStreamResponse(context.Background(), rec, strings.NewReader(tc.stream), false, time.Now(), &streamMetrics{})

			var ue *upstreamError
			if !errors.As(err, &ue) {
				t.Fatalf("expected *upstreamError so fallback can retry, got %T: %v", err, err)
			}
			if ue.StatusCode != http.StatusBadGateway {
				t.Errorf("expected 502, got %d", ue.StatusCode)
			}
			// The commit boundary: no event-stream header may have been written.
			if ct := rec.Header().Get("Content-Type"); ct != "" {
				t.Errorf("headers were committed before the turn was known good: Content-Type=%q", ct)
			}
			if rec.Body.Len() != 0 {
				t.Errorf("expected nothing written to the client, got %q", rec.Body.String())
			}
		})
	}
}

// TestHandleStreamResponse_ReplaysPeekedPayloads pins that peeking does not
// cost the stream any content. The forwarder re-frames every event as
// `data: <payload>\n\n`, so byte-level fidelity is the peek layer's contract
// (stream_peek_test.go); what has to hold here is that no payload is dropped,
// duplicated or reordered on the way out.
func TestHandleStreamResponse_ReplaysPeekedPayloads(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	stream := ": keep-alive\r\n" +
		"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\r\n\r\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\r\n\r\n" +
		"data: [DONE]\r\n\r\n"

	rec := httptest.NewRecorder()
	if err := h.handleStreamResponse(context.Background(), rec, strings.NewReader(stream), false, time.Now(), &streamMetrics{}); err != nil {
		t.Fatalf("a healthy stream must be served, got: %v", err)
	}

	want := []string{
		`{"choices":[{"delta":{"role":"assistant"}}]}`,
		`{"choices":[{"delta":{"content":"hi"}}]}`,
		`[DONE]`,
	}
	body := rec.Body.String()
	wantBody := ""
	for _, p := range want {
		wantBody += "data: " + p + "\n\n"
	}
	if body != wantBody {
		t.Errorf("payloads changed in flight\nwant %q\ngot  %q", wantBody, body)
	}
}

// TestHandleStreamResponse_RefusesErrorEventBeforeCommit covers the failure
// that survives even a healthy-looking status: a provider injecting an error
// frame. Before the commit it is still a retryable failure; after, it can only
// be relayed.
func TestHandleStreamResponse_RefusesErrorEventBeforeCommit(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	stream := "event: error\ndata: {\"error\":{\"code\":429,\"message\":\"model overloaded\"}}\n\n"

	rec := httptest.NewRecorder()
	err := h.handleStreamResponse(context.Background(), rec, strings.NewReader(stream), false, time.Now(), &streamMetrics{})

	var ue *upstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *upstreamError, got %T: %v", err, err)
	}
	if ue.StatusCode != http.StatusTooManyRequests {
		t.Errorf("expected the provider's own 429 to survive, got %d", ue.StatusCode)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "" {
		t.Errorf("headers were committed before the turn was known good: Content-Type=%q", ct)
	}
}

// TestHandleStreamResponse_ServesOpaquePayloads guards the clamp that keeps the
// peek from breaking providers it does not model. Anthropic-native SSE and
// vendor event vocabularies are not Chat Completions envelopes, so they must
// commit rather than be read as silence.
func TestHandleStreamResponse_ServesOpaquePayloads(t *testing.T) {
	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	stream := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"hi\"}}\n\n"

	rec := httptest.NewRecorder()
	if err := h.handleStreamResponse(context.Background(), rec, strings.NewReader(stream), false, time.Now(), &streamMetrics{}); err != nil {
		t.Fatalf("an opaque event vocabulary must be relayed, got: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "content_block_delta") {
		t.Errorf("expected the stream to reach the client, got %q", rec.Body.String())
	}
}
