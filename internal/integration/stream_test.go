//go:build integration

package integration

import (
	"bufio"
	"bytes"
	"net/http"
	"strings"
	"testing"
)

// TestStreamCompletionRelaysEvents pins the streaming contract end to end: the
// client asks for SSE, the gateway asks the upstream for SSE, the events reach
// the client in order, and the stream is terminated with [DONE]. A client that
// never sees [DONE] waits until its own timeout, which is the single most
// common "the gateway hangs" report.
func TestStreamCompletionRelaysEvents(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, streamResponder())
	env.AddConnection(t, "conn-deepseek", "deepseek", "DeepSeek Integration", upstream, "sk-upstream")

	res := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", true))
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	if got := res.Header.Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}

	payloads := sseDataLines(t, res.Body)
	if len(payloads) == 0 {
		t.Fatal("stream carried no data frames")
	}
	if last := payloads[len(payloads)-1]; last != "[DONE]" {
		t.Errorf("last SSE frame = %q, want the [DONE] sentinel", last)
	}
	if !strings.Contains(string(res.Body), "streamed ") {
		t.Errorf("stream body missing the upstream content delta: %q", truncate(res.Body))
	}

	sent := upstream.Last(t)
	if got := sent.Header.Get("Accept"); !strings.Contains(got, "text/event-stream") {
		t.Errorf("upstream Accept = %q, want text/event-stream for a streaming request", got)
	}
	if got := sent.Model(t); got != "deepseek-chat" {
		t.Errorf("upstream model = %q, want \"deepseek-chat\"", got)
	}
}

// TestStreamPassesThroughProviderErrorEnvelope pins that an upstream that fails
// before the first byte still yields a well-formed OpenAI error to the client
// instead of an empty 200 or a bare HTML error page — SSE clients parse the
// body as JSON events and would report a confusing parse failure.
func TestStreamPassesThroughProviderErrorEnvelope(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, JSONResponder(http.StatusTooManyRequests,
		`{"error":{"message":"slow down","type":"rate_limit_error","code":"rate_limit_exceeded"}}`))
	env.AddConnection(t, "conn-deepseek", "deepseek", "DeepSeek Integration", upstream, "sk-upstream")

	res := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", true))
	if res.Status != http.StatusTooManyRequests {
		t.Fatalf("POST /v1/chat/completions = %d, want 429 (body: %s)", res.Status, truncate(res.Body))
	}
	if msg := res.ErrorMessage(t); !strings.Contains(msg, "slow down") {
		t.Errorf("error message = %q, want the upstream reason", msg)
	}
}

// sseDataLines extracts the payload of every "data:" frame in an SSE body.
func sseDataLines(t *testing.T, body []byte) []string {
	t.Helper()
	var payloads []string
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		payloads = append(payloads, payload)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan SSE body: %v", err)
	}
	return payloads
}

// TestStreamMidStreamDropReportsInBandError pins issue #57 end to end.
// The HTTP 200 and the first content delta are already on the wire when the
// provider dies, so an in-band frame is the only way to tell the client. Before
// this the gateway just closed the socket and SDKs kept the truncated text as a
// finished answer.
func TestStreamMidStreamDropReportsInBandError(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, midStreamDropResponder())
	env.AddConnection(t, "conn-deepseek", "deepseek", "DeepSeek Integration", upstream, "sk-upstream")

	res := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", true))
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions = %d, want 200", res.Status)
	}
	body := string(res.Body)
	if !strings.Contains(body, "the answer is") {
		t.Errorf("delta that made it through is missing: %q", truncate(res.Body))
	}

	frame, ok := sseErrorFrame(t, res.Body)
	if !ok {
		t.Fatalf("client was never told the stream failed: %q", truncate(res.Body))
	}
	if frame.Error.Code == "" {
		t.Errorf("error frame carries no machine-readable code: %q", truncate(res.Body))
	}
	// A dropped stream must not be dressed up as a finished turn: that is
	// exactly what made the truncation invisible to clients before.
	if strings.Contains(body, "finish_reason") {
		t.Errorf("dropped stream must not carry a finish_reason: %q", truncate(res.Body))
	}
	if !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Errorf("client read loop must close with [DONE]: %q", truncate(res.Body))
	}
}
func midStreamDropResponder() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"the answer is\"}}]}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Kill the connection mid-turn.
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, err := hj.Hijack()
			if err == nil {
				_ = conn.Close()
				return
			}
		}
		panic(http.ErrAbortHandler)
	}
}
