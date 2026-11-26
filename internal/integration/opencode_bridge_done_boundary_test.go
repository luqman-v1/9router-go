//go:build integration

package integration

import (
	"context"

	"bufio"
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	json "encoding/json/v2"

	"9router/proxy/internal/proxy/executor"
)

// Regression for the free-tier-model-watch cron failures of 2026-11-19:
// hy4-flash-free wrapped the caller tool call as execute/CALL_TOOL, the bridge
// held the finish_reason line until [DONE], then flushed it with a single \n
// right before the sentinel. A strict OpenAI client joins adjacent data lines
// into one event, parsed chunk+[DONE] as one JSON payload and aborted the turn
// with provider_stream_non_json_data ("Extra data").
//
// This drives the production router against a fake free-tier gate emitting
// exactly that upstream pattern, and parses the client-visible stream the way
// the OpenAI SDK does: data lines of one event join with \n, each event must
// be either [DONE] or a single valid JSON document.
func TestOpenCodeBridgeDoneBoundary_StrictClientParsesStream(t *testing.T) {
	executor.ResetOpenCodeRetryCooldownForTesting()
	t.Cleanup(executor.ResetOpenCodeRetryCooldownForTesting)

	// The upstream frames mirror the live capture: the execute call, the
	// terminal tool_calls chunk, then `data: [DONE]` with NO blank line in
	// between — the exact shape the real gate emits on hy4-flash-free.
	callArgs := `{"code":"CALL_TOOL web_search {\"query\":\"new free AI model tier\"}"}`
	startChunk := `{"id":"gen-repro","object":"chat.completion.chunk","created":1,"model":"hy4-flash-free","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"execute","arguments":` + jsonString(t, callArgs) + `}}]}}]}`
	finishChunk := `{"id":"gen-repro","object":"chat.completion.chunk","created":1,"model":"hy4-flash-free","choices":[{"index":0,"finish_reason":"tool_calls","delta":{"role":"assistant","content":""}}]}`
	upstreamBody := "data: " + startChunk + "\n\n" +
		"data: " + finishChunk + "\n" +
		"data: [DONE]\n\n"

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(upstreamBody))
	}))
	t.Cleanup(up.Close)

	env := newEnv(t)
	priority := 1
	data := fmt.Sprintf(`{"apiKey":"public","baseUrl":%q}`, up.URL+"/inference/v1/chat/completions")
	if err := env.Repo.CreateProviderConnectionFull(context.Background(), "conn-bridge-done", "opencode", "apikey", "OpenCode", &priority, data); err != nil {
		t.Fatalf("create connection: %v", err)
	}

	res := env.Post(t, "/v1/chat/completions", map[string]any{
		"model":  "oc/hy4-flash-free",
		"stream": true,
		"messages": []map[string]any{
			{"role": "user", "content": "Search for new free AI model tiers."},
		},
		"tools": []map[string]any{{
			"type": "function",
			"function": map[string]any{
				"name":        "web_search",
				"description": "Search the web",
				"parameters":  map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}, "required": []string{"query"}},
			},
		}},
	})
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", res.Status, truncate(res.Body))
	}

	events := sseEvents(t, res.Body)
	if len(events) == 0 {
		t.Fatal("client received no SSE events")
	}
	sawCall, sawFinish, sawDone := false, false, false
	for _, payload := range events {
		if payload == "[DONE]" {
			sawDone = true
			continue
		}
		// The strict-client contract: every non-sentinel event is ONE valid
		// JSON document. The bug made this payload `chunkJSON\n[DONE]`.
		var chunk struct {
			Choices []struct {
				FinishReason *string `json:"finish_reason"`
				Delta        struct {
					ToolCalls []struct {
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("event is not a single JSON document (the Extra-data bug): %v\npayload: %q", err, payload)
		}
		for _, c := range chunk.Choices {
			if c.FinishReason != nil && *c.FinishReason == "tool_calls" {
				sawFinish = true
			}
			for _, tc := range c.Delta.ToolCalls {
				if tc.Function.Name == "web_search" {
					sawCall = true
					if !strings.Contains(tc.Function.Arguments, "new free AI model tier") {
						t.Errorf("rewritten call lost its arguments: %q", tc.Function.Arguments)
					}
				}
				if tc.Function.Name == "execute" {
					t.Errorf("execute name leaked to the client: %q", payload)
				}
			}
		}
	}
	if !sawCall {
		t.Error("caller never received the rewritten web_search tool call")
	}
	if !sawFinish {
		t.Error("terminal finish_reason tool_calls frame missing")
	}
	if !sawDone {
		t.Error("stream did not end with a [DONE] event")
	}
}

// jsonString encodes s as a JSON string literal (with quotes).
func jsonString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// sseEvents splits an SSE body into event payloads exactly as the OpenAI SDK
// does: lines accumulate until a blank line, data values join with \n.
func sseEvents(t *testing.T, body []byte) []string {
	t.Helper()
	var events []string
	var dataLines []string
	flush := func() {
		if len(dataLines) > 0 {
			events = append(events, strings.Join(dataLines, "\n"))
			dataLines = nil
		}
	}
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "data:"):
			v := strings.TrimPrefix(line, "data:")
			dataLines = append(dataLines, strings.TrimPrefix(v, " "))
		}
	}
	flush()
	return events
}
