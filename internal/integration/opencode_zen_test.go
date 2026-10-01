//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"
)

// OpenCode Zen answers three endpoints off one key, and a request must reach
// the one the requested model actually lives on. These cases go through the
// production router with a fake upstream, so a lane that silently fell back to
// /chat/completions fails here instead of as an upstream 404 in production.

func addZenConnection(t *testing.T, env *Env, up *Upstream) {
	t.Helper()
	addZenConnectionWithData(t, env, up.URL+"/zen/v1/chat/completions")
}

// addZenConnectionWithData stores the connection with a caller-chosen baseUrl
// so a test can repoint the same connection without re-seeding it.
func addZenConnectionWithData(t *testing.T, env *Env, baseURL string) {
	t.Helper()
	priority := 1
	data := `{"apiKey":"sk-zen","baseUrl":"` + baseURL + `"}`
	if err := env.Repo.CreateProviderConnectionFull("conn-ocz", "opencode-zen", "apikey", "OpenCode Zen", &priority, data); err != nil {
		t.Fatalf("create connection: %v", err)
	}
	stored, err := env.Repo.GetProviderConnectionByID("conn-ocz")
	if err != nil || stored == nil || !strings.Contains(stored.Data, baseURL) {
		t.Fatalf("connection did not persist the fake upstream URL: %v %+v", err, stored)
	}
}

func TestOpenCodeZenRoutesChatLane(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, chatCompletionResponder())
	addZenConnection(t, env, up)

	res := env.Post(t, "/v1/chat/completions", ChatBody("ocz/deepseek-v4-pro", false))
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, body %s", res.Status, truncate(res.Body))
	}
	if got := up.Last(t).Path; got != "/zen/v1/chat/completions" {
		t.Errorf("upstream path = %q, want /zen/v1/chat/completions", got)
	}
}

func TestOpenCodeZenRoutesMessagesLane(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"zen-messages-ok"}],"usage":{"input_tokens":5,"output_tokens":3}}`))
	})
	addZenConnection(t, env, up)

	// A Chat Completions client asking for a Claude model: the answer comes
	// from /messages, so the gateway has to translate on the way in and out.
	res := env.Post(t, "/v1/chat/completions", ChatBody("ocz/claude-sonnet-4-6", false))
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, body %s", res.Status, truncate(res.Body))
	}
	if got := up.Last(t).Path; got != "/zen/v1/messages" {
		t.Errorf("upstream path = %q, want /zen/v1/messages", got)
	}
	if !strings.Contains(string(res.Body), "zen-messages-ok") {
		t.Errorf("client must see the upstream text, got %s", truncate(res.Body))
	}
}

func TestOpenCodeZenClaudeClientKeepsMessagesFormat(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"native-messages-ok"}],"usage":{"input_tokens":5,"output_tokens":3}}`))
	})
	addZenConnection(t, env, up)

	res := env.Post(t, "/v1/messages", claudeMessagesBody("ocz/claude-sonnet-4-6", false))
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, body %s", res.Status, truncate(res.Body))
	}
	sent := up.Last(t)
	if sent.Path != "/zen/v1/messages" {
		t.Errorf("upstream path = %q, want /zen/v1/messages", sent.Path)
	}
	if !strings.Contains(string(sent.Body), "be terse") {
		t.Errorf("a Claude client body must reach the Messages endpoint untouched, got %s", truncate(sent.Body))
	}
	if sent.Header.Get("x-api-key") != "sk-zen" {
		t.Errorf("Messages lane must use the raw x-api-key auth, got %q", sent.Header.Get("x-api-key"))
	}
	if !strings.Contains(string(res.Body), "native-messages-ok") {
		t.Errorf("client must see the upstream text, got %s", truncate(res.Body))
	}
}

func TestOpenCodeZenRoutesResponsesLane(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_zen\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"zen-responses-ok\"}]}]}}\n\n"))
	})
	addZenConnection(t, env, up)

	// A /v1/responses client asking for Muse Spark: both sides speak Responses,
	// so the body must be relayed rather than round-tripped through chat.
	body := map[string]any{
		"model":  "ocz/muse-spark-1.3",
		"stream": true,
		"input": []map[string]any{
			{"type": "message", "role": "user", "content": []map[string]any{
				{"type": "input_text", "text": "ping"},
			}},
		},
	}
	res := env.Post(t, "/v1/responses", body)
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, body %s", res.Status, truncate(res.Body))
	}
	if got := up.Last(t).Path; got != "/zen/v1/responses" {
		t.Errorf("upstream path = %q, want /zen/v1/responses", got)
	}
	if !strings.Contains(string(res.Body), "response.completed") {
		t.Errorf("a native Responses relay must reach the client unchanged, got %s", truncate(res.Body))
	}
}

func TestOpenCodeZenResponsesClientOnChatLaneIsTranslated(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, chatCompletionResponder())
	addZenConnection(t, env, up)

	body := map[string]any{
		"model":  "ocz/deepseek-v4-pro",
		"stream": true,
		"input": []map[string]any{
			{"type": "message", "role": "user", "content": []map[string]any{
				{"type": "input_text", "text": "ping"},
			}},
		},
	}
	res := env.Post(t, "/v1/responses", body)
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, body %s", res.Status, truncate(res.Body))
	}
	sent := up.Last(t)
	if sent.Path != "/zen/v1/chat/completions" {
		t.Errorf("upstream path = %q, want /zen/v1/chat/completions", sent.Path)
	}
	if strings.Contains(string(sent.Body), `"input"`) {
		t.Errorf("the chat lane must receive messages[], not Responses input[]: %s", truncate(sent.Body))
	}
	if !strings.Contains(string(res.Body), "response.completed") {
		t.Errorf("the answer must still be replayed as Responses events, got %s", truncate(res.Body))
	}
}
