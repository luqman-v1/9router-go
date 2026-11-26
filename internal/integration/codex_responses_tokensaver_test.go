//go:build integration

package integration

import (
	"context"

	"net/http"
	"strings"
	"testing"

	json "encoding/json/v2"

	"9router/proxy/internal/app"
	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers"
)

// A /v1/responses client on a Responses-speaking upstream with a token saver
// active. The saver has to add its system turn as a Responses item: the Codex
// backend answers `400 Unknown parameter: 'input[0].content'` for the Chat
// shape `{"role":"system","content":"<text>"}` a messages[] injector writes,
// and that 400 takes the whole request down (issue #307; upstream
// decolua/9router carries the same failure as #2497).

// cavemanSaver turns the one token saver that injects a system prompt, leaving
// RTK off so the outbound body differs only by the injected turn.
func cavemanSaver(repo *db.Repo) *handlers.TokenSaverConfig {
	return app.ProvideTokenSaverConfig(repo, app.CLIParams{
		RTK: false, RTKSet: true,
		Caveman: true, CavemanSet: true,
		Ponytail: false, PonytailSet: true,
	})
}

// codexResponsesEnv boots a gateway whose codex executor points at a fake
// upstream. baseURL must end in /responses: that is what keeps the provider in
// its native Responses lane instead of translating the request to chat.
func codexResponsesEnv(t *testing.T, build func(*db.Repo) *handlers.TokenSaverConfig) (*Env, *Upstream) {
	t.Helper()
	env := newEnvWithTokenSaver(t, build)

	up := env.NewUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_codex\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"codex-ok\"}]}]}}\n\n"))
	})

	priority := 1
	data := `{"apiKey":"sk-codex","baseUrl":"` + up.URL + `/responses"}`
	if err := env.Repo.CreateProviderConnectionFull(context.Background(), "conn-codex", "codex", "apikey", "Codex Integration", &priority, data); err != nil {
		t.Fatalf("create codex connection: %v", err)
	}
	stored, err := env.Repo.GetProviderConnectionByID("conn-codex")
	if err != nil || stored == nil || !strings.Contains(stored.Data, up.URL) {
		t.Fatalf("codex connection did not persist the fake upstream URL: %v %+v", err, stored)
	}
	return env, up
}

// assertCodexInputShape fails on any input[] item the Codex backend rejects: an
// item type it does not know, or a message whose `content` is not an array of
// content blocks. The lite `additional_tools` prefix item is a real item the
// executor adds for these models and carries no content at all.
func assertCodexInputShape(t *testing.T, body []byte) {
	t.Helper()
	var payload struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode upstream body %q: %v", truncate(body), err)
	}
	if len(payload.Input) == 0 {
		t.Fatalf("upstream body has an empty input[]: %s", truncate(body))
	}
	for i, item := range payload.Input {
		kind, _ := item["type"].(string)
		switch kind {
		case "additional_tools":
			continue
		case "message":
			if _, ok := item["content"].([]any); !ok {
				t.Errorf("input[%d].content is %T, want an array of content blocks: %v", i, item["content"], item["content"])
			}
		default:
			t.Errorf("input[%d].type = %q, want \"message\" (Codex rejects a bare Chat turn): %s", i, kind, truncate(body))
		}
	}
}

// codexResponsesBody is a minimal /v1/responses request from a client that keeps
// its own system turn inside input[], which is the shape that used to break.
func codexResponsesBody() map[string]any {
	return map[string]any{
		"model":  "cx/gpt-6-sol",
		"stream": true,
		"input": []map[string]any{
			{"type": "message", "role": "developer", "content": []map[string]any{
				{"type": "input_text", "text": "be terse"},
			}},
			{"type": "message", "role": "user", "content": []map[string]any{
				{"type": "input_text", "text": "ping"},
			}},
		},
	}
}

// The control: with the saver off the same request already arrives in a shape
// Codex accepts, so the assertion in the saver case below can only be failing
// because of what the injection wrote.
func TestCodexResponsesClientWithoutTokenSaverKeepsValidInputShape(t *testing.T) {
	env, upstream := codexResponsesEnv(t, nil)

	res := env.Post(t, "/v1/responses", codexResponsesBody())
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/responses = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	sent := upstream.Last(t)
	if sent.Path != "/responses" {
		t.Errorf("upstream path = %q, want /responses", sent.Path)
	}
	assertCodexInputShape(t, sent.Body)
}

// The regression: the saver's system turn has to land as a Responses message
// item. A Chat-shaped turn here is the 400 in issue #307.
func TestCodexResponsesClientWithTokenSaverKeepsValidInputShape(t *testing.T) {
	env, upstream := codexResponsesEnv(t, cavemanSaver)

	res := env.Post(t, "/v1/responses", codexResponsesBody())
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/responses = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}

	sent := upstream.Last(t)
	assertCodexInputShape(t, sent.Body)
	if !strings.Contains(string(sent.Body), "ping") {
		t.Errorf("the client prompt must survive the token saver: %s", truncate(sent.Body))
	}
	if !strings.Contains(string(res.Body), "response.completed") {
		t.Errorf("client must receive the Responses stream, got %s", truncate(res.Body))
	}
}
