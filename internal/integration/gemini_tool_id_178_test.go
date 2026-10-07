//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	json "encoding/json/v2"
)

// vertexClaudeResponder impersonates the one thing a fake upstream can
// meaningfully add here: Vertex Anthropic's schema check. A plain 200 stub
// would pass even with the bug present, because the id is only ever validated
// by Google's side. So this responder rejects a Gemini body whose
// functionCall lacks an id, with the exact 400 the issue reports:
//
//	messages.1.content.0.tool_use.id: Field required
//
// The body comes from up.Last: NewUpstream already drained r.Body before
// calling this handler, so reading it again here would yield nothing.
func vertexClaudeResponder(t *testing.T, up **Upstream) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, _ *http.Request) {
		var probe struct {
			Request struct {
				Contents []struct {
					Role  string `json:"role"`
					Parts []struct {
						FunctionCall *struct {
							Name string `json:"name"`
							ID   string `json:"id"`
						} `json:"functionCall"`
					} `json:"parts"`
				} `json:"contents"`
			} `json:"request"`
		}
		if err := json.Unmarshal((*up).Last(t).Body, &probe); err != nil {
			http.Error(w, "bad body: "+err.Error(), http.StatusBadRequest)
			return
		}
		for i, c := range probe.Request.Contents {
			for j, p := range c.Parts {
				if p.FunctionCall != nil && p.FunctionCall.ID == "" {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_, _ = fmt.Fprintf(w,
						`{"type":"error","error":{"type":"invalid_request_error","message":%q}}`,
						fmt.Sprintf("messages.%d.content.%d.tool_use.id: Field required", i, j))
					return
				}
			}
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(geminiSSE))
	}
}

// TestAntigravityClaudeToolHistoryCarriesIDs pins #178 end to end. Claude behind
// Antigravity rebuilds a tool_use block per functionCall and 400s without an id,
// which killed every tool-calling session; a combo only hid it by falling
// through to the next model.
func TestAntigravityClaudeToolHistoryCarriesIDs(t *testing.T) {
	env := newEnv(t)
	// &up resolves when the request arrives, which is after NewUpstream returns.
	var up *Upstream
	up = env.NewUpstream(t, vertexClaudeResponder(t, &up))
	addAntigravityConnection(t, env, up)

	body := map[string]any{
		"model": "antigravity/claude-opus-4-6-thinking",
		"messages": []any{
			map[string]any{"role": "user", "content": "List files."},
			map[string]any{"role": "assistant", "content": "", "tool_calls": []any{
				map[string]any{"id": "call_abc123", "type": "function",
					"function": map[string]any{"name": "bash", "arguments": `{"command":"ls"}`}},
			}},
			map[string]any{"role": "tool", "tool_call_id": "call_abc123", "content": "file1.txt"},
			map[string]any{"role": "user", "content": "How many files?"},
		},
	}

	res := env.Post(t, "/v1/chat/completions", body)
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}

	sent := lastRequestBody(t, up)
	if strings.Contains(sent, "tool_use.id") {
		t.Fatalf("upstream rejected the tool history: %s", truncate([]byte(sent)))
	}
	// The response must answer the call by the same id, or Vertex cannot pair
	// the two blocks even though both now carry one.
	if n := strings.Count(sent, `"call_abc123"`); n != 2 {
		t.Errorf("id appears %d times in the upstream body, want 2 (one per call/response pair):\n%s",
			n, truncate([]byte(sent)))
	}
	if strings.Contains(sent, "__ts__") {
		t.Errorf("thought-signature transport suffix leaked to the wire:\n%s", truncate([]byte(sent)))
	}
}

// TestAntigravityComboDoesNotFallThroughOnToolHistory guards the symptom a user
// actually reports: a combo silently skipping Claude. When the tool history is
// malformed the gateway must reach exactly the one member, not retry onward.
func TestAntigravityComboDoesNotFallThroughOnToolHistory(t *testing.T) {
	env := newEnv(t)
	var up *Upstream
	up = env.NewUpstream(t, vertexClaudeResponder(t, &up))
	addAntigravityConnection(t, env, up)
	env.AddCombo(t, "combo-178", "Claude Tooling", []string{"antigravity/claude-opus-4-6-thinking"})

	body := map[string]any{
		"model": "Claude Tooling",
		"messages": []any{
			map[string]any{"role": "user", "content": "List files."},
			map[string]any{"role": "assistant", "content": "", "tool_calls": []any{
				map[string]any{"id": "call_abc123", "type": "function",
					"function": map[string]any{"name": "bash", "arguments": `{"command":"ls"}`}},
			}},
			map[string]any{"role": "tool", "tool_call_id": "call_abc123", "content": "file1.txt"},
		},
	}

	res := env.Post(t, "/v1/chat/completions", body)
	if res.Status != http.StatusOK {
		t.Fatalf("combo POST = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	if up.Count() != 1 {
		t.Errorf("upstream saw %d requests, want exactly 1 — a fall-through would mean the combo skipped Claude", up.Count())
	}
}