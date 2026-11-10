//go:build integration

package integration

import (
	"bufio"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestChiMiddlewareStackDoesNotCompressStream proves that the gateway's chi
// router stack (RequestID, MaxBodyByPath, Recoverer, RequestLogger) never
// compresses a streamed SSE response, even when the client's request sends
// `Accept-Encoding: gzip`.
func TestChiMiddlewareStackDoesNotCompressStream(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, streamResponder())
	env.AddConnection(t, "conn-mw-gzip", "deepseek", "DeepSeek", up, "sk-upstream")

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, env.BaseURL+"/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek/deepseek-chat","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+env.APIKey)
	req.Header.Set("Accept", "text/event-stream")
	// Client specifically requests gzip:
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")

	resp, err := env.client.Do(req)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, truncate(raw))
	}

	// 1. Content-Encoding MUST NOT be gzip or compressed.
	if enc := resp.Header.Get("Content-Encoding"); enc != "" {
		t.Errorf("client received Content-Encoding = %q, want none (SSE stream must never be compressed by middleware)", enc)
	}

	// 2. The client must be able to read standard plain-text SSE lines directly.
	reader := bufio.NewReader(resp.Body)
	var firstLine string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		if strings.HasPrefix(line, "data: ") {
			firstLine = line
			break
		}
	}
	if !strings.HasPrefix(firstLine, "data: ") {
		t.Errorf("expected first line to start with 'data: ', got: %q", firstLine)
	}
}
