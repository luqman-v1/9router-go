//go:build ignore

package main

import (
	"fmt"
	"net/http"
	"os"
	"time"
)

// Mock upstream OpenAI-compatible server that returns a fixed SSE response.
func main() {
	port := os.Getenv("MOCK_PORT")
	if port == "" {
		port = "20199"
	}

	mux := http.NewServeMux()

	// Non-streaming endpoint
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		stream := r.URL.Query().Get("stream") == "true" ||
			r.Header.Get("Accept") == "text/event-stream"

		// Simulate small upstream latency
		time.Sleep(5 * time.Millisecond)

		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(200)
			flusher, _ := w.(http.Flusher)

			chunks := []string{
				`data: {"id":"mock-1","choices":[{"delta":{"role":"assistant","content":""},"index":0}],"model":"mock-model"}`,
				`data: {"id":"mock-1","choices":[{"delta":{"content":"Hello"},"index":0}],"model":"mock-model"}`,
				`data: {"id":"mock-1","choices":[{"delta":{"content":" world"},"index":0}],"model":"mock-model"}`,
				`data: {"id":"mock-1","choices":[{"delta":{},"finish_reason":"stop","index":0}],"model":"mock-model"}`,
				`data: [DONE]`,
			}
			for _, chunk := range chunks {
				fmt.Fprintf(w, "%s\n\n", chunk)
				if flusher != nil {
					flusher.Flush()
				}
			}
			return
		}

		// Non-streaming JSON response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		fmt.Fprint(w, `{"id":"mock-1","choices":[{"message":{"role":"assistant","content":"Hello world"},"finish_reason":"stop","index":0}],"model":"mock-model","usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`)
	})

	// Anthropic-style endpoint
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		fmt.Fprint(w, `{"id":"mock-msg-1","type":"message","role":"assistant","content":[{"type":"text","text":"Hello world"}],"model":"mock-model","stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":2}}`)
	})

	fmt.Printf("Mock upstream listening on :%s\n", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start mock upstream: %v\n", err)
		os.Exit(1)
	}
}
