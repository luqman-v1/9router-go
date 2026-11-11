//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	json "encoding/json/v2"
)

// Ordinary coding clients need a classic Responses request so the backend can
// emit more than one tool call. Exercise the production router, not just the
// body converter: both the HTTP header and the body shape determine the mode.
func TestCodexClassicParallelToolsThroughRouter(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range codexInterleavedEvents {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", event)
		}
	})
	priority := 1
	data := fmt.Sprintf(`{"apiKey":"sk-codex","baseUrl":%q}`, up.URL+"/responses")
	if err := env.Repo.CreateProviderConnectionFull("conn-codex-classic", "codex", "apikey", "Codex", &priority, data); err != nil {
		t.Fatalf("create Codex connection: %v", err)
	}
	stored, err := env.Repo.GetProviderConnectionByID("conn-codex-classic")
	if err != nil || stored == nil || !strings.Contains(stored.Data, up.URL+"/responses") {
		t.Fatalf("Codex fake upstream URL was not persisted: connection=%+v error=%v", stored, err)
	}

	for _, model := range []string{"gpt-5.6-luna", "gpt-5.6-sol", "gpt-6-luna", "gpt-6.1-sol"} {
		t.Run(model, func(t *testing.T) {
			body := ChatBody("cx/"+model, false)
			body["tools"] = []any{
				map[string]any{"type": "function", "function": map[string]any{"name": "lookup_document", "parameters": map[string]any{"type": "object"}}},
				map[string]any{"type": "function", "function": map[string]any{"name": "list_entries", "parameters": map[string]any{"type": "object"}}},
			}
			res := env.Post(t, "/v1/chat/completions", body)
			if res.Status != http.StatusOK {
				t.Fatalf("status = %d: %s", res.Status, res.Body)
			}
			checkInterleavedChat(t, res)
			req := up.Last(t)
			if got := req.Model(t); got != model {
				t.Fatalf("upstream model = %q, want %q", got, model)
			}
			if got := req.Header.Get("X-OpenAI-Internal-Codex-Responses-Lite"); got != "" {
				t.Errorf("unexpected Lite header %q", got)
			}
			var payload struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
				Input []struct {
					Type string `json:"type"`
				} `json:"input"`
				Parallel *bool `json:"parallel_tool_calls"`
			}
			if err := json.Unmarshal(req.Body, &payload); err != nil {
				t.Fatalf("decode upstream body: %v", err)
			}
			if len(payload.Tools) != 2 {
				t.Errorf("top-level tools = %d, want 2", len(payload.Tools))
			}
			if payload.Parallel != nil && !*payload.Parallel {
				t.Error("parallel_tool_calls was disabled")
			}
			for _, item := range payload.Input {
				if item.Type == "additional_tools" {
					t.Error("classic request unexpectedly contains Lite additional_tools")
				}
			}
		})
	}
}

func TestCodexResponsesClientChoosesWireShape(t *testing.T) {
	tests := []struct {
		name  string
		model string
		lite  bool
	}{
		{name: "5.6 luna uses classic", model: "gpt-5.6-luna"},
		{name: "5.6 sol uses classic", model: "gpt-5.6-sol"},
		{name: "6 luna uses classic", model: "gpt-6-luna"},
		{name: "explicit additional_tools stays lite", model: "gpt-6-luna", lite: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, up := codexResponsesEnv(t, nil)
			body := codexResponsesBody()
			body["model"] = "cx/" + tt.model
			tool := map[string]any{"type": "function", "name": "lookup_document", "parameters": map[string]any{"type": "object"}}
			if tt.lite {
				body["input"] = []map[string]any{
					{"type": "additional_tools", "role": "developer", "tools": []any{tool}},
					{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "ping"}}},
				}
				body["parallel_tool_calls"] = false
			} else {
				body["tools"] = []any{tool}
			}
			res := env.Post(t, "/v1/responses", body)
			if res.Status != http.StatusOK {
				t.Fatalf("status = %d: %s", res.Status, res.Body)
			}
			req := up.Last(t)
			var sent struct {
				Tools []any `json:"tools"`
				Input []struct {
					Type string `json:"type"`
				} `json:"input"`
				Parallel *bool `json:"parallel_tool_calls"`
			}
			if err := json.Unmarshal(req.Body, &sent); err != nil {
				t.Fatalf("decode upstream body: %v", err)
			}
			hasPrefix := false
			for _, item := range sent.Input {
				hasPrefix = hasPrefix || item.Type == "additional_tools"
			}
			if hasPrefix != tt.lite {
				t.Errorf("additional_tools present = %v, want %v", hasPrefix, tt.lite)
			}
			if got := req.Header.Get("X-OpenAI-Internal-Codex-Responses-Lite") == "true"; got != tt.lite {
				t.Errorf("Lite header present = %v, want %v", got, tt.lite)
			}
			if tt.lite && (sent.Parallel == nil || *sent.Parallel) {
				t.Error("Lite must explicitly disable parallel_tool_calls")
			}
			if !tt.lite && (len(sent.Tools) != 1 || sent.Parallel != nil && !*sent.Parallel) {
				t.Errorf("classic request lost tools or disabled parallel calls: %s", req.Body)
			}
		})
	}
}

func TestCodexClassicHonorsParallelToolCallsFalse(t *testing.T) {
	env, up := codexResponsesEnv(t, nil)
	for _, path := range []string{"/v1/chat/completions", "/v1/responses"} {
		t.Run(path, func(t *testing.T) {
			var body map[string]any
			if path == "/v1/responses" {
				body = codexResponsesBody()
			} else {
				body = ChatBody("cx/gpt-6-luna", true)
			}
			body["model"] = "cx/gpt-6-luna"
			body["parallel_tool_calls"] = false
			res := env.Post(t, path, body)
			if res.Status != http.StatusOK {
				t.Fatalf("status = %d: %s", res.Status, res.Body)
			}
			req := up.Last(t)
			if got := req.Header.Get("X-OpenAI-Internal-Codex-Responses-Lite"); got != "" {
				t.Errorf("unexpected Lite header %q", got)
			}
			var sent struct {
				Parallel *bool `json:"parallel_tool_calls"`
			}
			if err := json.Unmarshal(req.Body, &sent); err != nil {
				t.Fatalf("decode upstream body: %v", err)
			}
			if sent.Parallel == nil || *sent.Parallel {
				t.Errorf("client's parallel_tool_calls=false not preserved: %s", req.Body)
			}
		})
	}
}
