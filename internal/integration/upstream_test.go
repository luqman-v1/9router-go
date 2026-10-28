//go:build integration

package integration

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	json "encoding/json/v2"
)

// RecordedRequest is one request an Upstream observed.
type RecordedRequest struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

// Model reports the "model" field of a JSON request body.
func (r RecordedRequest) Model(t *testing.T) string {
	t.Helper()
	var payload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(r.Body, &payload); err != nil {
		t.Fatalf("decode upstream request body %q: %v", truncate(r.Body), err)
	}
	return payload.Model
}

// Upstream is a fake LLM provider. It records every request the gateway makes
// to it and answers with whatever handler the test supplies, so integration
// tests assert on the real outbound envelope (URL, headers, rewritten model)
// without ever leaving the process.
type Upstream struct {
	*httptest.Server

	mu       sync.Mutex
	requests []RecordedRequest
}

// NewUpstream starts a fake provider that answers with handler. Tests that only
// need a canned 200 can use the responder helpers below.
func (e *Env) NewUpstream(t *testing.T, handler http.HandlerFunc) *Upstream {
	t.Helper()
	up := &Upstream{}
	up.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read upstream body: "+err.Error(), http.StatusInternalServerError)
			return
		}
		up.mu.Lock()
		up.requests = append(up.requests, RecordedRequest{
			Method: r.Method, Path: r.URL.Path, Header: r.Clone(r.Context()).Header, Body: body,
		})
		up.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(up.Close)
	return up
}

// Count is the number of requests this upstream received.
func (u *Upstream) Count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.requests)
}

// Last returns the most recent request, failing the test when there was none.
func (u *Upstream) Last(t *testing.T) RecordedRequest {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.requests) == 0 {
		t.Fatal("upstream received no requests")
	}
	return u.requests[len(u.requests)-1]
}

// JSONResponder answers every request with a fixed status and body. Useful for
// upstream error-mapping tests, where the point is the status the client sees.
func JSONResponder(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

// chatCompletionJSON is a well-formed OpenAI chat completion with usage, the
// shape the usage-tracking assertions read token counts from.
const chatCompletionJSON = `{
  "id": "chatcmpl-integration",
  "object": "chat.completion",
  "created": 1700000000,
  "model": "upstream-model",
  "choices": [
    {"index": 0, "message": {"role": "assistant", "content": "upstream reply"}, "finish_reason": "stop"}
  ],
  "usage": {"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18}
}`

// chatCompletionResponder answers with chatCompletionJSON and status 200.
func chatCompletionResponder() http.HandlerFunc {
	return JSONResponder(http.StatusOK, chatCompletionJSON)
}

// sseStreamBody is a minimal but complete OpenAI event stream: a role opener, a
// content delta, a finish_reason chunk carrying usage, and the [DONE] sentinel.
// Streaming clients (Claude Code, Codex, the OpenAI SDKs) hang or error out
// when any of those parts go missing, so the fake upstream sends all of them.
const sseStreamBody = "data: {\"id\":\"chatcmpl-stream\",\"object\":\"chat.completion.chunk\",\"model\":\"upstream-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"chatcmpl-stream\",\"object\":\"chat.completion.chunk\",\"model\":\"upstream-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"streamed \"},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"chatcmpl-stream\",\"object\":\"chat.completion.chunk\",\"model\":\"upstream-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"reply\"},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"chatcmpl-stream\",\"object\":\"chat.completion.chunk\",\"model\":\"upstream-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":13,\"completion_tokens\":5,\"total_tokens\":18}}\n\n" +
	"data: [DONE]\n\n"

// streamResponder answers with sseStreamBody.
func streamResponder() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, sseStreamBody)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}

// claudeMessagesBody builds a minimal Anthropic Messages request, the protocol
// Claude Code speaks.
func claudeMessagesBody(model string, stream bool) map[string]any {
	return map[string]any{
		"model":      model,
		"max_tokens": 512,
		"stream":     stream,
		"system":     "be terse",
		"messages": []map[string]any{
			{"role": "user", "content": "Summarize the Go memory model."},
		},
	}
}

// AddConnection stores a provider connection whose baseUrl points at up, which
// is what getProviderConfig prefers over the providers.KnownProviders registry
// entry. apiKey is the credential the gateway must present upstream.
//
// The connection is stored at priority 1 — every AddConnection call is, since
// the helper has no way to say otherwise. A test that needs one of two
// same-provider accounts picked first must therefore use
// AddConnectionWithPriority: with equal priorities the picker falls back to its
// documented tie-break (the most recently updated row wins), and updatedAt is
// written at second resolution by nowUTC, so such a test passes or fails on
// whether its two inserts happened to land in the same wall-clock second.
//
// The stored row is read back before returning: an empty baseUrl would fall
// through to the real provider URL from the registry and turn an offline suite
// into a live call, so a fixture that failed to persist is a hard failure here
// rather than a confusing 401 from a real provider in CI.
func (e *Env) AddConnection(t *testing.T, id, provider, name string, up *Upstream, apiKey string) {
	t.Helper()
	e.AddConnectionWithPriority(t, id, provider, name, up, apiKey, 1)
}

// AddConnectionWithPriority is AddConnection with an explicit pick priority, for
// a test that pins the order the picker walks same-provider accounts in. A lower
// number is picked first; equal priorities tie-break on updatedAt, which is not
// something a fixture should rely on.
func (e *Env) AddConnectionWithPriority(t *testing.T, id, provider, name string, up *Upstream, apiKey string, priority int) {
	t.Helper()
	data := fmt.Sprintf(`{"apiKey":%q,"baseUrl":%q}`, apiKey, up.URL+"/chat/completions")
	if err := e.Repo.CreateProviderConnectionFull(id, provider, "apikey", name, &priority, data); err != nil {
		t.Fatalf("create connection %s: %v", id, err)
	}
	stored, err := e.Repo.GetProviderConnectionByID(id)
	if err != nil {
		t.Fatalf("read back connection %s: %v", id, err)
	}
	if stored == nil {
		t.Fatalf("connection %s was not stored", id)
	}
	if !strings.Contains(stored.Data, up.URL) {
		t.Fatalf("connection %s stored data %q, want it to carry the fake upstream URL %q — a request "+
			"through it would dial the real provider", id, stored.Data, up.URL)
	}
}

// AddCombo stores a combo whose members are provider/model entries.
func (e *Env) AddCombo(t *testing.T, id, name string, models []string) {
	t.Helper()
	encoded, err := json.Marshal(models)
	if err != nil {
		t.Fatalf("encode combo models: %v", err)
	}
	if err := e.Repo.CreateCombo(id, name, "llm", string(encoded), "fallback"); err != nil {
		t.Fatalf("create combo %s: %v", name, err)
	}
}

// newProviderEnv is the common fixture: an Env with a single DeepSeek
// connection pointed at a fake upstream that answers with a canned completion.
// It returns both so a test can assert on what the gateway sent.
func newProviderEnv(t *testing.T) (*Env, *Upstream) {
	t.Helper()
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	env.AddConnection(t, "conn-deepseek", "deepseek", "DeepSeek Integration", upstream, "sk-upstream")
	return env, upstream
}
