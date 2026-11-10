//go:build integration

package integration

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestStreamAdvertisesIdentityEncoding pins the upstream gzip contract:
//
//   - streaming requests advertise Accept-Encoding: identity so an upstream
//     edge cannot answer with a buffered gzip event stream (which bursts the
//     whole turn at once instead of streaming token by token);
//   - a non-compliant upstream that answers gzip despite the identity request
//     is unwrapped transparently by UnwrapIdentityGzip so the client still
//     reads valid SSE events.
func TestStreamAdvertisesIdentityEncoding(t *testing.T) {
	t.Run("chat_stream_asks_for_identity", func(t *testing.T) {
		env := newEnv(t)
		up := env.NewUpstream(t, streamResponder())
		env.AddConnection(t, "conn-ident-chat", "deepseek", "DeepSeek", up, "sk-upstream")

		res := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", true))
		if res.Status != http.StatusOK {
			t.Fatalf("status = %d, body = %s", res.Status, truncate(res.Body))
		}
		if got := up.Last(t).Header.Get("Accept-Encoding"); got != "identity" {
			t.Errorf("upstream Accept-Encoding = %q, want identity", got)
		}
	})

	t.Run("non_stream_request_keeps_default_encoding", func(t *testing.T) {
		env := newEnv(t)
		up := env.NewUpstream(t, chatCompletionResponder())
		env.AddConnection(t, "conn-ident-nostream", "deepseek", "DeepSeek", up, "sk-upstream")

		res := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false))
		if res.Status != http.StatusOK {
			t.Fatalf("status = %d, body = %s", res.Status, truncate(res.Body))
		}
		// Non-streaming leaves the header unset so Go's transport adds gzip.
		if got := up.Last(t).Header.Get("Accept-Encoding"); !strings.Contains(got, "gzip") {
			t.Errorf("upstream Accept-Encoding = %q, want it to carry gzip (Go default)", got)
		}
	})

	t.Run("non_compliant_gzip_upstream_is_unwrapped", func(t *testing.T) {
		const chunks = 5
		const gap = 30 * time.Millisecond

		// An upstream that returns Content-Encoding: gzip despite identity.
		nonCompliantUpstream := func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Content-Encoding", "gzip")
			w.WriteHeader(http.StatusOK)
			flusher, _ := w.(http.Flusher)
			gz := gzip.NewWriter(w)
			defer gz.Close()

			emit := func(ev string) {
				_, _ = io.WriteString(gz, ev)
				_ = gz.Flush()
				if flusher != nil {
					flusher.Flush()
				}
			}
			emit(`data: {"id":"c","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}` + "\n\n")
			for i := range chunks {
				time.Sleep(gap)
				emit(fmt.Sprintf(`data: {"id":"c","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"t%d "},"finish_reason":null}]}`+"\n\n", i))
			}
			emit(`data: {"id":"c","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n")
			emit("data: [DONE]\n\n")
		}

		env := newEnv(t)
		up := env.NewUpstream(t, nonCompliantUpstream)
		env.AddConnection(t, "conn-gzip-rebel", "deepseek", "Rebel", up, "sk-upstream")

		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, env.BaseURL+"/v1/chat/completions",
			strings.NewReader(`{"model":"deepseek/deepseek-chat","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+env.APIKey)
		req.Header.Set("Accept", "text/event-stream")

		resp, err := env.client.Do(req)
		if err != nil {
			t.Fatalf("send: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(resp.Body)
			t.Fatalf("status = %d, body = %s", resp.StatusCode, truncate(raw))
		}

		// The client must receive readable UTF-8 text, never compressed bytes.
		reader := bufio.NewReader(resp.Body)
		var contentCount int
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				break
			}
			if strings.HasPrefix(line, "data: ") && strings.Contains(line, `"content":"t`) {
				contentCount++
			}
		}
		if contentCount != chunks {
			t.Errorf("saw %d content chunks, want %d", contentCount, chunks)
		}
	})
}
