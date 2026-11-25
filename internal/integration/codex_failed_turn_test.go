//go:build integration

package integration

import (
	"context"

	"net/http"
	"strings"
	"testing"
)

// codexSchemaErrorEvent is the shape the Codex backend uses for a rejected
// tool schema: the failure arrives as an event on an HTTP 200 stream.
const codexSchemaErrorEvent = `{"type":"error","error":{"type":"invalid_request_error","message":"Invalid schema for function 'Artifact': '^\\p{Cc}...' is not a 'regex'"}}`

// codexEnv boots a gateway whose codex connection points at a fake upstream
// running the given handler, and returns the env plus the upstream so a test
// can assert what was sent. Each caller needs its own env: a failed codex turn
// cools the connection down, and a later request through the same env would be
// answered by the picker without ever reaching the upstream.
func codexEnv(t *testing.T, handler http.HandlerFunc) *Env {
	t.Helper()
	env := newEnv(t)
	up := env.NewUpstream(t, handler)

	priority := 1
	data := `{"apiKey":"sk-codex","baseUrl":"` + up.URL + `/responses"}`
	if err := env.Repo.CreateProviderConnectionFull(context.Background(), "conn-codex", "codex", "apikey", "Codex Integration", &priority, data); err != nil {
		t.Fatalf("create codex connection: %v", err)
	}
	return env
}

// A codex turn that fails upstream must not reach the client as an empty
// success. The upstream commits HTTP 200 before the failure is known (the
// stream carries the error as an event), so the executor has to end the turn
// with its own error frame instead of a fabricated successful terminal:
// `finish_reason: "stop"` on the OpenAI wire and `stop_reason: "end_turn"` on
// the Claude one both read to a client as a completed answer with no text.
//
// Before the fix both clients received a 200 whose stream said the turn
// finished normally and contained no content at all — the empty response that
// opened #334, where Claude Code showed nothing and combo fallback settled on
// the model that had just failed.
func TestCodexFailedStreamIsNotServedAsEmptySuccess(t *testing.T) {
	failingUpstream := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: " + codexSchemaErrorEvent + "\n\ndata: [DONE]\n\n"))
	}

	t.Run("claude client sees an error frame, not a finished turn", func(t *testing.T) {
		env := codexEnv(t, failingUpstream)
		res := env.Post(t, "/v1/messages", map[string]any{
			"model":      "cx/gpt-6-sol",
			"max_tokens": 64,
			"stream":     true,
			"messages": []map[string]any{
				{"role": "user", "content": "ping"},
			},
		})

		if res.Status != http.StatusOK {
			t.Fatalf("POST /v1/messages = %d, want the committed 200 (body: %s)", res.Status, truncate(res.Body))
		}
		body := string(res.Body)
		if !strings.Contains(body, `"type":"error"`) {
			t.Errorf("no in-band error frame reached the Claude client: %s", truncate(res.Body))
		}
		if strings.Contains(body, "end_turn") {
			t.Errorf("failed turn was closed as a success (end_turn): %s", truncate(res.Body))
		}
	})

	t.Run("openai client sees an error frame, not finish_reason stop", func(t *testing.T) {
		env := codexEnv(t, failingUpstream)
		res := env.Post(t, "/v1/chat/completions", ChatBody("cx/gpt-6-sol", true))

		if res.Status != http.StatusOK {
			t.Fatalf("POST /v1/chat/completions = %d, want the committed 200 (body: %s)", res.Status, truncate(res.Body))
		}
		body := string(res.Body)
		if !strings.Contains(body, `"error"`) {
			t.Errorf("no in-band error frame reached the OpenAI client: %s", truncate(res.Body))
		}
		if strings.Contains(body, `"finish_reason":"stop"`) {
			t.Errorf("failed turn was closed as a success (finish_reason stop): %s", truncate(res.Body))
		}
	})
}

// The control: a healthy codex turn still streams its content and terminates
// normally, so the failed-turn branch above can only be reacting to the error.
func TestCodexHealthyStreamStillServesItsContent(t *testing.T) {
	env := codexEnv(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// A chat-completions client is served the translated stream, so the
		// answer has to arrive as the delta events a real turn emits — a bare
		// response.completed carries it only for the Responses passthrough.
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"codex-ok\"}\n\n" +
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_codex\",\"status\":\"completed\",\"output\":[]}}\n\n"))
	})

	res := env.Post(t, "/v1/chat/completions", ChatBody("cx/gpt-6-sol", true))
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions = %d (body: %s)", res.Status, truncate(res.Body))
	}
	body := string(res.Body)
	if !strings.Contains(body, "codex-ok") {
		t.Errorf("healthy turn lost its content: %s", truncate(res.Body))
	}
	if !strings.Contains(body, "[DONE]") {
		t.Errorf("healthy turn lost its terminator: %s", truncate(res.Body))
	}
}
