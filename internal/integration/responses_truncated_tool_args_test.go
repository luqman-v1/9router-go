//go:build integration

package integration

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// responsesBody builds a /v1/responses request carrying one function tool and
// forcing a call, which is the shape the issue reports.
func responsesBody(model string, stream bool) map[string]any {
	return map[string]any{
		"model":       model,
		"stream":      stream,
		"tool_choice": "required",
		"tools": []map[string]any{{
			"type":        "function",
			"name":        "exec_command",
			"description": "Run a shell command",
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{"cmd": map[string]any{"type": "string"}},
				"required":   []string{"cmd"},
			},
		}},
		"input": []map[string]any{{
			"role": "user",
			"content": []map[string]any{{
				"type": "input_text",
				"text": "Call exec_command once.",
			}},
		}},
	}
}

// truncateAtToken cuts a complete argument payload between two tokens, which is
// what a provider that drops the connection mid-arguments leaves behind.
func truncateAtToken(full string) string {
	return full[:strings.Index(full, `PY"}`)]
}

// toolCallStream answers a Chat Completions request with a streamed tool call
// whose argument buffer is cut mid-token and never finished: no finish_reason,
// no [DONE], the connection simply ends. That is issue #747's upstream.
func toolCallStream(t *testing.T, args string) http.HandlerFunc {
	t.Helper()
	var b strings.Builder
	b.WriteString(`data: {"id":"chatcmpl-trunc","object":"chat.completion.chunk","model":"upstream-model","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_trunc","type":"function","function":{"name":"exec_command","arguments":""}}]},"finish_reason":null}]}` + "\n\n")
	const fragment = 24
	for start := 0; start < len(args); start += fragment {
		end := min(start+fragment, len(args))
		payload := strings.ReplaceAll(args[start:end], `\`, `\\`)
		payload = strings.ReplaceAll(payload, `"`, `\"`)
		payload = strings.ReplaceAll(payload, "\n", `\n`)
		b.WriteString(`data: {"id":"chatcmpl-trunc","object":"chat.completion.chunk","model":"upstream-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"` + payload + `"}}]},"finish_reason":null}]}` + "\n\n")
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, b.String())
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}

// TestResponsesStreamTruncatedToolArgumentsEndIncomplete is issue #747 end to
// end: a /v1/responses client, a combo resolving to a Chat Completions upstream,
// and a stream cut mid tool-arguments. The client must be told the turn is
// incomplete; before the fix it was told the turn completed, ran the arguments
// it had, and failed to parse them with nothing to retry on.
func TestResponsesStreamTruncatedToolArgumentsEndIncomplete(t *testing.T) {
	env := newEnv(t)

	full := `{"cmd":"python3 - <<'PY'\n` + strings.Repeat(`# line: some varied sentence about robots\n`, 20) + `PY"}`
	upstream := env.NewUpstream(t, toolCallStream(t, truncateAtToken(full)))
	env.AddConnection(t, "conn-chat", "deepseek", "Chat Completions upstream", upstream, "sk-upstream")
	env.AddCombo(t, "combo-747", "combo-747", []string{"deepseek/deepseek-chat"})

	res := env.Post(t, "/v1/responses", responsesBody("combo-747", true))
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/responses = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}

	body := string(res.Body)
	if !strings.Contains(body, "event: response.incomplete") {
		t.Errorf("the client was not told the turn is incomplete:\n%s", truncate(res.Body))
	}
	if strings.Contains(body, `"type":"response.completed"`) {
		t.Errorf("a truncated turn was reported as completed:\n%s", truncate(res.Body))
	}
	if !strings.Contains(body, `"reason":"tool_call_arguments_incomplete"`) {
		t.Errorf("the terminal event does not name the reason:\n%s", truncate(res.Body))
	}

	// The upstream must actually have been reached, or this proves nothing about
	// the bridge: the gateway could have refused before forwarding.
	if got := upstream.Count(); got != 1 {
		t.Fatalf("upstream requests = %d, want 1", got)
	}
	if got := upstream.Last(t).Model(t); got != "deepseek-chat" {
		t.Errorf("upstream model = %q, want the bare model id", got)
	}
}

// TestResponsesStreamCompleteToolArgumentsStayCompleted is the other half: a
// tool call whose arguments parse must still close as response.completed, so the
// fix above cannot turn working turns into failures.
func TestResponsesStreamCompleteToolArgumentsStayCompleted(t *testing.T) {
	env := newEnv(t)

	upstream := env.NewUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-ok\",\"object\":\"chat.completion.chunk\",\"model\":\"upstream-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call_ok\",\"type\":\"function\",\"function\":{\"name\":\"exec_command\",\"arguments\":\"\"}}]},\"finish_reason\":null}]}\n\n"+
			"data: {\"id\":\"chatcmpl-ok\",\"object\":\"chat.completion.chunk\",\"model\":\"upstream-model\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"cmd\\\":\\\"ls -la\\\"}\"}}]},\"finish_reason\":null}]}\n\n"+
			"data: {\"id\":\"chatcmpl-ok\",\"object\":\"chat.completion.chunk\",\"model\":\"upstream-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":4}}\n\n"+
			"data: [DONE]\n\n")
	})
	env.AddConnection(t, "conn-chat", "deepseek", "Chat Completions upstream", upstream, "sk-upstream")
	env.AddCombo(t, "combo-747", "combo-747", []string{"deepseek/deepseek-chat"})

	res := env.Post(t, "/v1/responses", responsesBody("combo-747", true))
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/responses = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}

	body := string(res.Body)
	if !strings.Contains(body, "event: response.completed") {
		t.Errorf("a complete turn was not closed with response.completed:\n%s", truncate(res.Body))
	}
	if strings.Contains(body, "response.incomplete") {
		t.Errorf("a complete turn was reported as incomplete:\n%s", truncate(res.Body))
	}
}
