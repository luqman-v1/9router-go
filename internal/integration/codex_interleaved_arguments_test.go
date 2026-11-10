//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	json "encoding/json/v2"
)

// The done events are full snapshots, not additional argument fragments. The
// two calls overlap so a bridge must track arguments per call, not per stream.
var codexInterleavedEvents = []string{
	`{"type":"response.created","response":{"id":"resp_interleaved","object":"response","status":"in_progress","output":[]}}`,
	`{"type":"response.output_item.added","output_index":0,"item":{"id":"fc_read","type":"function_call","call_id":"call_read","name":"lookup_document","arguments":"","status":"in_progress"}}`,
	`{"type":"response.output_item.added","output_index":1,"item":{"id":"fc_search","type":"function_call","call_id":"call_search","name":"list_entries","arguments":"","status":"in_progress"}}`,
	`{"type":"response.function_call_arguments.delta","item_id":"fc_read","output_index":0,"delta":"{\"path\":"}`,
	`{"type":"response.function_call_arguments.delta","item_id":"fc_search","output_index":1,"delta":"{\"pattern\":"}`,
	`{"type":"response.function_call_arguments.delta","item_id":"fc_read","output_index":0,"delta":"\"/src/main.go\"}"}`,
	`{"type":"response.function_call_arguments.delta","item_id":"fc_search","output_index":1,"delta":"\"*.go\"}"}`,
	`{"type":"response.function_call_arguments.done","item_id":"fc_search","output_index":1,"arguments":"{\"pattern\":\"*.go\"}"}`,
	`{"type":"response.function_call_arguments.done","item_id":"fc_read","output_index":0,"arguments":"{\"path\":\"/src/main.go\"}"}`,
	`{"type":"response.output_item.done","output_index":1,"item":{"id":"fc_search","type":"function_call","call_id":"call_search","name":"list_entries","arguments":"{\"pattern\":\"*.go\"}","status":"completed"}}`,
	`{"type":"response.output_item.done","output_index":0,"item":{"id":"fc_read","type":"function_call","call_id":"call_read","name":"lookup_document","arguments":"{\"path\":\"/src/main.go\"}","status":"completed"}}`,
	`{"type":"response.completed","response":{"id":"resp_interleaved","object":"response","status":"completed","output":[{"id":"fc_read","type":"function_call","call_id":"call_read","name":"lookup_document","arguments":"{\"path\":\"/src/main.go\"}","status":"completed"},{"id":"fc_search","type":"function_call","call_id":"call_search","name":"list_entries","arguments":"{\"pattern\":\"*.go\"}","status":"completed"}],"usage":{"input_tokens":12,"output_tokens":8,"total_tokens":20}}}`,
}

type interleavedCall struct {
	id, name, arguments string
}

var interleavedWant = map[string]interleavedCall{
	"call_read":   {"call_read", "lookup_document", `{"path":"/src/main.go"}`},
	"call_search": {"call_search", "list_entries", `{"pattern":"*.go"}`},
}

func TestCodexInterleavedArgumentsClientFormats(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range codexInterleavedEvents {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", event)
		}
	})
	priority := 1
	data := fmt.Sprintf(`{"apiKey":"sk-codex","baseUrl":%q}`, up.URL+"/responses")
	if err := env.Repo.CreateProviderConnectionFull("conn-codex-interleaved", "codex", "apikey", "Codex", &priority, data); err != nil {
		t.Fatalf("create Codex connection: %v", err)
	}
	stored, err := env.Repo.GetProviderConnectionByID("conn-codex-interleaved")
	if err != nil || stored == nil || !strings.Contains(stored.Data, up.URL+"/responses") {
		t.Fatalf("Codex fake upstream URL was not persisted: connection=%+v error=%v", stored, err)
	}

	tests := []struct {
		name, path string
		body       any
		check      func(*testing.T, *Result)
	}{
		{"chat non-streaming", "/v1/chat/completions", ChatBody("cx/gpt-6-sol", false), checkInterleavedChat},
		{"messages streaming", "/v1/messages", claudeMessagesBody("cx/gpt-6-sol", true), checkInterleavedMessages},
		{"responses streaming passthrough", "/v1/responses", codexResponsesBody(), checkInterleavedResponses},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := env.Post(t, tt.path, tt.body)
			if res.Status != http.StatusOK {
				t.Fatalf("POST %s = %d: %s", tt.path, res.Status, res.Body)
			}
			if got := up.Count(); got != i+1 {
				t.Fatalf("upstream requests = %d, want %d", got, i+1)
			}
			if got := up.Last(t).Model(t); got != "gpt-6-sol" {
				t.Fatalf("upstream model = %q, want gpt-6-sol", got)
			}
			tt.check(t, res)
		})
	}
}

func checkInterleavedChat(t *testing.T, res *Result) {
	t.Helper()
	var response struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	res.Decode(t, &response)
	if len(response.Choices) != 1 {
		t.Fatalf("chat choices = %d, want 1: %s", len(response.Choices), res.Body)
	}
	calls := make(map[string]interleavedCall)
	for _, call := range response.Choices[0].Message.ToolCalls {
		if _, exists := calls[call.ID]; exists {
			t.Fatalf("duplicate chat call ID %q: %s", call.ID, res.Body)
		}
		calls[call.ID] = interleavedCall{call.ID, call.Function.Name, call.Function.Arguments}
	}
	if !reflect.DeepEqual(calls, interleavedWant) {
		t.Errorf("chat calls = %#v, want %#v; response: %s", calls, interleavedWant, res.Body)
	}
}

func checkInterleavedMessages(t *testing.T, res *Result) {
	t.Helper()
	blocks := make(map[int]interleavedCall)
	for _, payload := range sseDataLines(t, res.Body) {
		if payload == "[DONE]" {
			continue
		}
		var event struct {
			Type  string `json:"type"`
			Index int    `json:"index"`
			Block struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content_block"`
			Delta struct {
				Type        string `json:"type"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			t.Fatalf("invalid Messages event %q: %v", payload, err)
		}
		if event.Type == "content_block_start" && event.Block.Type == "tool_use" {
			if _, exists := blocks[event.Index]; exists {
				t.Fatalf("duplicate tool block index %d: %s", event.Index, res.Body)
			}
			blocks[event.Index] = interleavedCall{id: event.Block.ID, name: event.Block.Name}
		}
		if event.Type == "content_block_delta" && event.Delta.Type == "input_json_delta" {
			block, exists := blocks[event.Index]
			if !exists {
				t.Fatalf("tool delta before start at index %d: %s", event.Index, res.Body)
			}
			block.arguments += event.Delta.PartialJSON
			blocks[event.Index] = block
		}
	}
	calls := make(map[string]interleavedCall)
	for _, block := range blocks {
		if _, exists := calls[block.id]; exists {
			t.Fatalf("duplicate Messages tool ID %q: %s", block.id, res.Body)
		}
		calls[block.id] = block
	}
	if !reflect.DeepEqual(calls, interleavedWant) {
		t.Errorf("Messages calls = %#v, want %#v; stream: %s", calls, interleavedWant, res.Body)
	}
}

func checkInterleavedResponses(t *testing.T, res *Result) {
	t.Helper()
	// Compare every upstream data payload, in order, including all four deltas,
	// both arguments.done snapshots, and both output_item.done snapshots.
	// This checks the original Responses events only, not stream termination.
	var got []string
	for _, payload := range sseDataLines(t, res.Body) {
		if payload == "[DONE]" {
			continue
		}
		var event struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			t.Fatalf("invalid Responses event %q: %v", payload, err)
		}
		if strings.HasPrefix(event.Type, "response.") {
			got = append(got, payload)
		}
	}
	if !reflect.DeepEqual(got, codexInterleavedEvents) {
		t.Errorf("Responses upstream events changed:\ngot  %#v\nwant %#v", got, codexInterleavedEvents)
	}
}
