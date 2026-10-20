//go:build ignore

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// mockWantsStream reads the requested mode from the JSON request body. The
// query string and Accept header alone were not enough: a gateway that
// streams because the body says so would still be measured as JSON, and the
// benchmark would report a latency for a response shape the client never
// asked for.
func mockWantsStream(w http.ResponseWriter, r *http.Request) (bool, error) {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	var body struct {
		Stream bool `json:"stream"`
	}
	if err := decoder.Decode(&body); err != nil {
		return false, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return false, fmt.Errorf("expected one JSON request document")
	}
	return body.Stream || r.URL.Query().Get("stream") == "true" || r.Header.Get("Accept") == "text/event-stream", nil
}

// mockFeatures are the request features the mock answers differently. The
// benchmark matrix needs a tool-call turn, a reasoning turn and a plain turn
// to travel the same wire, so the mock has to reflect what was asked for
// instead of always returning the same completion.
type mockFeatures struct {
	HasTools  bool
	Reasoning bool
	Model     string
}

// detectMockFeatures reads the request body once and reports which answer
// shape the turn asks for. A body it cannot parse is not an error here: the
// mock answers with the plain shape, and a malformed request is the gateway's
// problem to report, not the mock's.
func detectMockFeatures(body []byte) mockFeatures {
	var req struct {
		Model           string `json:"model"`
		Tools           []any  `json:"tools"`
		ReasoningEffort string `json:"reasoning_effort"`
		IncludeReasoning bool   `json:"include_reasoning"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return mockFeatures{}
	}
	f := mockFeatures{
		HasTools:  len(req.Tools) > 0,
		Reasoning: req.ReasoningEffort != "" || req.IncludeReasoning || strings.Contains(req.Model, "reasoning"),
		Model:     req.Model,
	}
	return f
}

// writeSSE writes one SSE frame. The event name is omitted when empty, which
// is how the OpenAI chat wire carries its frames.
func writeSSE(w http.ResponseWriter, flusher http.Flusher, event, data string) {
	if event != "" {
		fmt.Fprintf(w, "event: %s\n", event)
	}
	fmt.Fprintf(w, "data: %s\n\n", data)
	if flusher != nil {
		flusher.Flush()
	}
}

// Mock upstream OpenAI-compatible server that returns a fixed SSE response.
func main() {
	port := os.Getenv("MOCK_PORT")
	if port == "" {
		port = "20199"
	}

	mux := http.NewServeMux()

	// Non-streaming endpoint
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "invalid mock request", http.StatusBadRequest)
			return
		}
		var body struct {
			Stream bool `json:"stream"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			http.Error(w, "invalid mock request", http.StatusBadRequest)
			return
		}
		stream := body.Stream || r.URL.Query().Get("stream") == "true" || r.Header.Get("Accept") == "text/event-stream"
		features := detectMockFeatures(raw)

		// Simulate small upstream latency
		time.Sleep(5 * time.Millisecond)

		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(200)
			flusher, _ := w.(http.Flusher)

			if features.Reasoning {
				writeSSE(w, flusher, "", `{"id":"mock-1","choices":[{"delta":{"reasoning_content":"thinking..."},"index":0}],"model":"mock-model"}`)
			}
			writeSSE(w, flusher, "", `{"id":"mock-1","choices":[{"delta":{"role":"assistant","content":""},"index":0}],"model":"mock-model"}`)
			if features.HasTools {
				writeSSE(w, flusher, "", `{"id":"mock-1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_mock_1","type":"function","function":{"name":"mock_tool","arguments":"{\"city\":\"Paris\"}"}}]},"index":0}],"model":"mock-model"}`)
			} else {
				writeSSE(w, flusher, "", `{"id":"mock-1","choices":[{"delta":{"content":"Hello"},"index":0}],"model":"mock-model"}`)
				writeSSE(w, flusher, "", `{"id":"mock-1","choices":[{"delta":{"content":" world"},"index":0}],"model":"mock-model"}`)
			}
			finish := "stop"
			if features.HasTools {
				finish = "tool_calls"
			}
			writeSSE(w, flusher, "", fmt.Sprintf(`{"id":"mock-1","choices":[{"delta":{},"finish_reason":%q,"index":0}],"model":"mock-model"}`, finish))
			writeSSE(w, flusher, "", "data: [DONE]")
			return
		}

		// Non-streaming JSON response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		message := `{"role":"assistant","content":"Hello world"}`
		if features.Reasoning {
			message = `{"role":"assistant","content":"Hello world","reasoning_content":"thinking..."}`
		}
		finish := "stop"
		toolCalls := ""
		if features.HasTools {
			finish = "tool_calls"
			toolCalls = `,"tool_calls":[{"id":"call_mock_1","type":"function","function":{"name":"mock_tool","arguments":"{\"city\":\"Paris\"}"}}]`
		}
		fmt.Fprintf(w, `{"id":"mock-1","choices":[{"message":%s%s,"finish_reason":%q,"index":0}],"model":"mock-model","usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`, message, toolCalls, finish)
	})

	// Anthropic-style endpoint
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "invalid mock request", http.StatusBadRequest)
			return
		}
		var body struct {
			Stream bool `json:"stream"`
			Tools  []any `json:"tools"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			http.Error(w, "invalid mock request", http.StatusBadRequest)
			return
		}
		hasTools := len(body.Tools) > 0

		time.Sleep(5 * time.Millisecond)

		if body.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(200)
			flusher, _ := w.(http.Flusher)

			writeSSE(w, flusher, "message_start", `{"type":"message_start","message":{"id":"mock-msg-1","type":"message","role":"assistant","model":"mock-model","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":0}}}`)
			if hasTools {
				writeSSE(w, flusher, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_mock_1","name":"mock_tool","input":{}}}`)
				writeSSE(w, flusher, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"Paris\"}"}}`)
				writeSSE(w, flusher, "content_block_stop", `{"type":"content_block_stop","index":0}`)
			} else {
				writeSSE(w, flusher, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
				writeSSE(w, flusher, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello world"}}`)
				writeSSE(w, flusher, "content_block_stop", `{"type":"content_block_stop","index":0}`)
			}
			writeSSE(w, flusher, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`)
			writeSSE(w, flusher, "message_stop", `{"type":"message_stop"}`)
			return
		}

		// Non-streaming JSON response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		content := `[{"type":"text","text":"Hello world"}]`
		if hasTools {
			content = `[{"type":"tool_use","id":"toolu_mock_1","name":"mock_tool","input":{"city":"Paris"}}]`
		}
		fmt.Fprintf(w, `{"id":"mock-msg-1","type":"message","role":"assistant","content":%s,"model":"mock-model","stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":2}}`, content)
	})

	// OpenAI Responses-style endpoint
	mux.HandleFunc("/v1/responses", func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "invalid mock request", http.StatusBadRequest)
			return
		}
		var body struct {
			Stream bool `json:"stream"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			http.Error(w, "invalid mock request", http.StatusBadRequest)
			return
		}
		stream := body.Stream || r.URL.Query().Get("stream") == "true" || r.Header.Get("Accept") == "text/event-stream"

		time.Sleep(5 * time.Millisecond)

		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(200)
			flusher, _ := w.(http.Flusher)

			writeSSE(w, flusher, "response.created", `{"type":"response.created","response":{"id":"resp_mock-1","object":"response","status":"in_progress","model":"mock-model","output":[]}}`)
			writeSSE(w, flusher, "response.output_text.delta", `{"type":"response.output_text.delta","delta":"Hello world"}`)
			writeSSE(w, flusher, "response.completed", `{"type":"response.completed","response":{"id":"resp_mock-1","object":"response","status":"completed","model":"mock-model","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello world"}]}],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}}`)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		fmt.Fprint(w, `{"id":"resp_mock-1","object":"response","status":"completed","model":"mock-model","output":[{"id":"msg_mock_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello world"}]}],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}`)
	})

	fmt.Printf("Mock upstream listening on 127.0.0.1:%s\n", port)
	if err := http.ListenAndServe("127.0.0.1:"+port, mux); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start mock upstream: %v\n", err)
		os.Exit(1)
	}
}
