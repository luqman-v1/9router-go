//go:build integration

package integration

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	"9router/proxy/internal/db"
)

// addStrictPool stores a pool whose proxy refuses every tunnel, so a request
// that reaches the provider anyway has left over the real IP.
func addStrictPool(t *testing.T, env *Env, name string) string {
	t.Helper()
	proxySrv, _ := newCountingProxy(t)
	t.Cleanup(func() {})
	created, err := env.Repo.InsertProxyPool(db.ProxyPoolData{
		Name: name, ProxyURL: proxySrv.URL, Type: "http", StrictProxy: true,
	})
	if err != nil {
		t.Fatalf("create strict pool: %v", err)
	}
	return created["id"].(string)
}

// TestStrictPoolNeverFallsBackToTheProviderDirectly is the #4333 leak seen
// from outside the gateway. A connection bound to a strict pool whose proxies
// are all dead must answer with an error; a 200 carrying the provider's own
// reply is proof the request escaped over the real IP instead.
func TestStrictPoolNeverFallsBackToTheProviderDirectly(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	env.AddConnection(t, "conn-ds", "deepseek", "DeepSeek", upstream, "sk-upstream")
	poolID := addStrictPool(t, env, "pool-strict")
	assignPool(t, env, "conn-ds", poolID)
	if err := env.Repo.UpdateProxyPool(poolID, map[string]any{"strictProxy": true}); err != nil {
		t.Fatalf("mark pool strict: %v", err)
	}

	chat := env.Post(t, "/v1/chat/completions", ChatBody("ds/deepseek-chat", false))
	if chat.Status == http.StatusOK {
		t.Fatalf("a strict pool with dead proxies must not answer 200 from the provider: %s", truncate(chat.Body))
	}
	if upstream.Count() != 0 {
		t.Errorf("the provider answered %d times: strict traffic escaped over the real IP", upstream.Count())
	}
}

// TestNonStrictPoolStillDegradesToDirect keeps the ordinary case honest. A
// pool that is not marked strict still falls back to the direct connection
// when its proxy refuses, which is what keeps a dead proxy from taking a
// provider down. The counting proxy must therefore have been dialed first.
func TestNonStrictPoolStillDegradesToDirect(t *testing.T) {
	proxySrv, hits := newCountingProxy(t)
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	env.AddConnection(t, "conn-ds", "deepseek", "DeepSeek", upstream, "sk-upstream")
	poolID := addProxyPool(t, env, "pool-lax", proxySrv.URL)
	assignPool(t, env, "conn-ds", poolID)

	chat := env.Post(t, "/v1/chat/completions", ChatBody("ds/deepseek-chat", false))
	if hits.Load() == 0 {
		t.Errorf("the assigned pool was never dialed (status %d): %s", chat.Status, truncate(chat.Body))
	}
}

// stalledSSE answers with the answer and finish_reason and then holds the
// connection open, without the usage trailer the terminal event waits on. It
// releases on request context cancellation as well as on cleanup: a handler
// that stays blocked makes httptest.Server.Close wait out its grace period on
// every run.
type stalledSSE struct {
	release chan struct{}
	once    sync.Once
}

func (s *stalledSSE) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"stalled\"}}]}\n\n"))
	_, _ = w.Write([]byte("data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"))
	w.(http.Flusher).Flush()
	// An SSE comment completes a read and would count as an event, so a stall
	// has to be silence.
	select {
	case <-r.Context().Done():
	case <-s.release:
	}
}

func (s *stalledSSE) Close() { s.once.Do(func() { close(s.release) }) }

// TestResponsesClientGetsTerminalEventWhenUpstreamStallsAfterFinish pins the
// watchdog through the real gateway. The fake upstream sends the answer and
// finish_reason — everything a client needs — then holds the connection open
// without the usage trailer the terminal event waits on. Without a bound the
// client waits forever.
func TestResponsesClientGetsTerminalEventWhenUpstreamStallsAfterFinish(t *testing.T) {
	stalled := &stalledSSE{release: make(chan struct{})}
	t.Cleanup(stalled.Close)

	env := newEnv(t)
	upstream := env.NewUpstream(t, stalled.ServeHTTP)
	env.AddConnection(t, "conn-ds", "deepseek", "DeepSeek", upstream, "sk-upstream")

	res := env.Post(t, "/v1/responses", map[string]any{"model": "ds/deepseek-chat", "input": "hi", "stream": true})
	stalled.Close()
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/responses = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	if got := strings.Count(string(res.Body), `"type":"response.completed"`); got != 1 {
		t.Errorf("response.completed count = %d, want exactly 1: %s", got, truncate(res.Body))
	}
	if !strings.Contains(string(res.Body), `"text":"stalled"`) {
		t.Errorf("the terminal event must carry the answer that already streamed: %s", truncate(res.Body))
	}
}
