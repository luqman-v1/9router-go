//go:build integration

package integration

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

func TestParityStreamRelay(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, streamResponder())
	env.AddConnection(t, "parity-deepseek", "deepseek", "Parity", up, "synthetic")
	got := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", true))
	if got.Status != http.StatusOK {
		t.Fatalf("status=%d body=%s", got.Status, got.Body)
	}
	if !strings.HasPrefix(got.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatal("response is not SSE")
	}
	if up.Count() != 1 || !bytes.Contains(got.Body, []byte("streamed ")) ||
		!bytes.Contains(got.Body, []byte("reply")) || bytes.Count(got.Body, []byte("data: [DONE]")) != 1 {
		t.Fatalf("calls=%d body=%s", up.Count(), got.Body)
	}
	if got := up.Last(t).Model(t); got != "deepseek-chat" {
		t.Fatalf("outbound model=%q", got)
	}
}
