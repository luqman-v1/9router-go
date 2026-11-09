package chat

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A streaming combo whose every member fails must answer with SSE frames a
// client can actually parse: each event starts with "data: " and ends with two
// REAL newline bytes (0x0A 0x0A), never the two characters backslash+'n'.
//
// The failure mode this pins: an upstream that answers a stream request with a
// JSON error body. The gateway relays the body, and the frame separator the
// client needs to split events on has to be real bytes. A literal "\n\n" in
// the response makes the whole body one unparseable line, so an SSE client
// (oh-my-pi on Bun) reports `JSON Parse error: Unrecognized token '\'` and the
// real error — status, type, code — is never seen.
//
// The stream request goes through the same combo loop the non-streaming one
// does, so the terminal error must arrive as an event stream, not as a bare
// JSON document under an event-stream Content-Type.
func TestStreamingComboFailureFramesUseRealNewlines(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":"upstream_unavailable","message":"Provider unavailable. Try again."}}`))
	}))
	defer upstream.Close()
	repo := seedComboWithTwoUpstreams(t, "combo-stream-fail", upstream.URL, upstream.URL, "combo-stream-fail")
	rec := httptest.NewRecorder()
	NewChatHandler(repo).HandleChatCompletions(rec,
		httptest.NewRequest("POST", "/v1/chat/completions",
			strings.NewReader(`{"model":"combo-stream-fail","messages":[{"role":"user","content":"hi"}],"stream":true}`)))

	body := rec.Body.String()
	if body == "" {
		t.Fatalf("empty body; status=%d ct=%q", rec.Code, rec.Header().Get("Content-Type"))
	}

	// (b) No event may carry the two-character escape sequence. A client that
	// splits on real newlines sees the whole body as one line and fails on the
	// backslash.
	if strings.Contains(body, `\n`) {
		t.Errorf("response carries a literal backslash-n escape; SSE frames must use real 0x0A bytes\nbody: %q", body)
	}

	// (a) The frame separator is two real newline bytes.
	if !strings.Contains(body, "\n\n") {
		t.Errorf("response carries no real double newline; SSE frames cannot be split\nbody: %q", body)
	}

	// (c) Every event starts with "data: " and ends with two real newlines.
	// A trailing fragment without its terminator is a truncated event.
	for _, event := range strings.Split(body, "\n\n") {
		if strings.TrimSpace(event) == "" {
			continue
		}
		if !strings.HasPrefix(event, "data: ") {
			t.Errorf("event does not start with \"data: \": %q", event)
		}
	}
	if trimmed := strings.TrimRight(body, "\n"); trimmed != "" && !strings.HasSuffix(body, "\n\n") {
		t.Errorf("last event is not terminated by two real newlines\nbody: %q", body)
	}

	// The error must survive the framing: a client that parses the payload has
	// to be able to read why the turn failed.
	if !strings.Contains(body, "upstream_unavailable") && !strings.Contains(body, "upstream_error") {
		t.Errorf("no error code reached the client\nbody: %q", body)
	}
}
