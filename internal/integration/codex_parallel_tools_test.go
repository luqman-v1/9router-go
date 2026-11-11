//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	json "encoding/json/v2"
)

// Codex sends argument deltas followed by snapshots in arguments.done and
// output_item.done. A client concatenates chat deltas, so those snapshots must
// not be appended a second time, even when multiple calls are interleaved.
func TestCodexParallelToolArgumentsReachChatClientOnce(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []string{
			`{"type":"response.output_item.added","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"read","arguments":""}}`,
			`{"type":"response.output_item.added","output_index":1,"item":{"id":"fc_2","type":"function_call","call_id":"call_2","name":"glob","arguments":""}}`,
			`{"type":"response.function_call_arguments.delta","item_id":"fc_1","call_id":"call_1","name":"read","output_index":0,"delta":"{\"path\":"}`,
			`{"type":"response.function_call_arguments.delta","item_id":"fc_2","output_index":1,"delta":"{\"pattern\":"}`,
			`{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"\"/home/pestit\"}"}`,
			`{"type":"response.function_call_arguments.delta","item_id":"fc_2","output_index":1,"delta":"\"*\"}"}`,
			`{"type":"response.function_call_arguments.done","item_id":"fc_1","output_index":0,"arguments":"{\"path\":\"/home/pestit\"}"}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"read","arguments":"{\"path\":\"/home/pestit\"}"}}`,
			`{"type":"response.function_call_arguments.done","item_id":"fc_2","output_index":1,"arguments":"{\"pattern\":\"*\"}"}`,
			`{"type":"response.output_item.done","output_index":1,"item":{"id":"fc_2","type":"function_call","call_id":"call_2","name":"glob","arguments":"{\"pattern\":\"*\"}"}}`,
			`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}`,
		} {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", event)
		}
	})
	priority := 1
	data := `{"apiKey":"sk-codex","baseUrl":"` + up.URL + `/responses"}`
	if err := env.Repo.CreateProviderConnectionFull("conn-codex", "codex", "apikey", "Codex Integration", &priority, data); err != nil {
		t.Fatalf("create codex connection: %v", err)
	}

	res := env.Post(t, "/v1/chat/completions", ChatBody("cx/gpt-6-sol", true))
	if res.Status != http.StatusOK {
		t.Fatalf("chat status = %d, body: %s", res.Status, truncate(res.Body))
	}
	if got := up.Last(t).Model(t); got != "gpt-6-sol" {
		t.Fatalf("upstream model = %q", got)
	}
	type call struct {
		Index    int    `json:"index"`
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	assembled := map[int]call{}
	for _, payload := range sseDataLines(t, res.Body) {
		if payload == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					ToolCalls []call `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("invalid SSE payload %q: %v", payload, err)
		}
		for _, choice := range chunk.Choices {
			for _, part := range choice.Delta.ToolCalls {
				current := assembled[part.Index]
				current.ID += part.ID
				current.Function.Name += part.Function.Name
				current.Function.Arguments += part.Function.Arguments
				assembled[part.Index] = current
			}
		}
	}
	if len(assembled) != 2 {
		t.Fatalf("got %d calls: %#v; stream: %s", len(assembled), assembled, res.Body)
	}
	for i, want := range []struct{ id, name, arguments string }{
		{"call_1", "read", `{"path":"/home/pestit"}`},
		{"call_2", "glob", `{"pattern":"*"}`},
	} {
		got := assembled[i]
		if got.ID != want.id || got.Function.Name != want.name || got.Function.Arguments != want.arguments {
			t.Errorf("call %d = %+v, want %+v; stream: %s", i, got, want, strings.TrimSpace(string(res.Body)))
		}
	}
}
