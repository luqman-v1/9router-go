//go:build integration

package integration

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"9router/proxy/internal/db"
)

// newCountingProxy returns a proxy that counts every request it is asked to
// tunnel and refuses them all. A request that reached the provider directly
// never passed through it, so the counter is the only honest way to assert an
// assigned pool is actually used.
func newCountingProxy(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, "proxy refuses to tunnel", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func addProxyPool(t *testing.T, env *Env, name, proxyURL string) string {
	t.Helper()
	created, err := env.Repo.InsertProxyPool(db.ProxyPoolData{Name: name, ProxyURL: proxyURL, Type: "http"})
	if err != nil {
		t.Fatalf("create proxy pool: %v", err)
	}
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("pool has no id: %v", created)
	}
	if stored, gerr := env.Repo.GetProxyPool(id); gerr != nil || stored == nil {
		t.Fatalf("proxy pool %s was not stored: %v", id, gerr)
	}
	return id
}

func assignPool(t *testing.T, env *Env, connID, poolID string) {
	t.Helper()
	res := env.Put(t, "/api/connections/"+connID, map[string]any{
		"proxyPoolId":          poolID,
		"providerSpecificData": map[string]any{"proxyPoolId": poolID},
	})
	if res.Status != http.StatusOK {
		t.Fatalf("assign pool status = %d: %s", res.Status, truncate(res.Body))
	}
}

// addNoAuthConnection gives a no-auth provider a stored, credential-free
// connection so it is served through the normal picker, which is what a real
// no-auth deployment looks like. The assigned pool must be honoured exactly as
// it is for a keyed connection.
func addNoAuthConnection(t *testing.T, env *Env, id, provider, name string) {
	t.Helper()
	priority := 1
	if err := env.Repo.CreateProviderConnectionFull(id, provider, "none", name, &priority, `{}`); err != nil {
		t.Fatalf("create %s connection: %v", provider, err)
	}
}

func setProviderStrategy(t *testing.T, env *Env, key, poolID string) {
	t.Helper()
	var decoded map[string]any
	raw := `{"` + key + `":{"proxyPoolId":"` + poolID + `"}}`
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("decode settings fragment %q: %v", raw, err)
	}
	if err := env.Repo.UpdateSettingsRaw(map[string]any{"providerStrategies": decoded}); err != nil {
		t.Fatalf("write settings: %v", err)
	}
}

// The reported bug: a pool is assigned from the dashboard, the row shows it
// bound, and the traffic still leaves directly. The cases are pinned by the
// upstream path the request must take, not by the model name, so the test can
// never reach opencode.ai by accident — that lane is free and a proxy bypass
// would otherwise answer 200 straight from upstream.
func TestAssignedProxyPoolIsActuallyDialed(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		model    string
		// wiring installs the connection (or the provider-level strategy) for
		// the case and returns nothing: the pool id is needed by both.
		wire func(t *testing.T, env *Env, poolID string)
	}{
		{
			name:     "connection level, provider without a custom executor",
			provider: "deepseek",
			model:    "ds/deepseek-chat",
			wire: func(t *testing.T, env *Env, poolID string) {
				upstream := env.NewUpstream(t, chatCompletionResponder())
				env.AddConnection(t, "conn-ds", "deepseek", "DeepSeek", upstream, "sk-upstream")
				assignPool(t, env, "conn-ds", poolID)
			},
		},
		{
			name:     "connection level, provider with a custom executor",
			provider: "kiro",
			model:    "kiro/glm-5",
			wire: func(t *testing.T, env *Env, poolID string) {
				upstream := env.NewUpstream(t, chatCompletionResponder())
				env.AddConnection(t, "conn-kiro", "kiro", "Kiro", upstream, "sk-upstream")
				assignPool(t, env, "conn-kiro", poolID)
			},
		},
		{
			name:     "provider level, connection bound while the pool is stored under ocg",
			provider: "opencode-go",
			model:    "opencode-go/deepseek-flash",
			wire: func(t *testing.T, env *Env, poolID string) {
				upstream := env.NewUpstream(t, chatCompletionResponder())
				env.AddConnection(t, "conn-ocg", "opencode-go", "OpenCode Go", upstream, "sk-upstream")
				setProviderStrategy(t, env, "ocg", poolID)
			},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			proxySrv, hits := newCountingProxy(t)
			env := newEnv(t)
			tt.wire(t, env, addProxyPool(t, env, "pool-a", proxySrv.URL))

			chat := env.Post(t, "/v1/chat/completions", ChatBody(tt.model, false))
			if hits.Load() == 0 {
				t.Errorf("the assigned proxy pool was never dialed for %s: the request bypassed it (status %d): %s",
					tt.provider, chat.Status, truncate(chat.Body))
			}
		})
	}
}

// A provider that bypasses the shared client — the MiMo free lane builds its
// own HTTP calls — has to honour a provider-level pool too, or the assignment
// the dashboard shows is decorative.
func TestProviderLevelPoolReachesBespokeTransport(t *testing.T) {
	proxySrv, hits := newCountingProxy(t)
	env := newEnv(t)
	poolID := addProxyPool(t, env, "pool-a", proxySrv.URL)
	setProviderStrategy(t, env, "mmf", poolID)

	chat := env.Post(t, "/v1/chat/completions", ChatBody("mmf/mimo-v2.5-free", false))
	if hits.Load() == 0 {
		t.Errorf("the provider-level pool was never dialed on the MiMo lane (status %d): %s", chat.Status, truncate(chat.Body))
	}
}

// A bound but inactive pool must not silently become "no proxy": the
// assignment is visible in the dashboard, so a request that cannot use it has
// to fail rather than leave directly.
func TestInactiveProxyPoolDoesNotSilentlyGoDirect(t *testing.T) {
	proxySrv, _ := newCountingProxy(t)
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	env.AddConnection(t, "conn-ds", "deepseek", "DeepSeek", upstream, "sk-upstream")
	poolID := addProxyPool(t, env, "pool-a", proxySrv.URL)
	assignPool(t, env, "conn-ds", poolID)
	if err := env.Repo.UpdateProxyPool(poolID, map[string]any{"isActive": false}); err != nil {
		t.Fatalf("deactivate pool: %v", err)
	}

	chat := env.Post(t, "/v1/chat/completions", ChatBody("ds/deepseek-chat", false))
	if chat.Status == http.StatusOK {
		t.Errorf("a bound but inactive pool must not fall back to a direct 200: %s", truncate(chat.Body))
	}
}

// proxyStampingSurvivesTheHop proves the assignment end to end: the origin
// answers only when the request actually travelled through the assigned proxy.
func TestProxyStampingSurvivesTheHop(t *testing.T) {
	seen := make(chan string, 4)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case seen <- r.Header.Get("X-Via-Proxy"):
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","created":1,"model":"deepseek-chat",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"through-proxy"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer origin.Close()

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outReq, err := http.NewRequestWithContext(r.Context(), r.Method, origin.URL+r.URL.Path, r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		outReq.Header.Set("X-Via-Proxy", "assigned")
		resp, err := origin.Client().Do(outReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		copyBody(w, resp.Body)
	}))
	defer proxy.Close()

	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	env.AddConnection(t, "conn-ds", "deepseek", "DeepSeek", upstream, "sk-upstream")
	assignPool(t, env, "conn-ds", addProxyPool(t, env, "pool-a", proxy.URL))

	chat := env.Post(t, "/v1/chat/completions", ChatBody("ds/deepseek-chat", false))
	if chat.Status != http.StatusOK {
		t.Fatalf("status = %d, body %s", chat.Status, truncate(chat.Body))
	}
	select {
	case got := <-seen:
		if got != "assigned" {
			t.Errorf("origin saw X-Via-Proxy=%q, want the value stamped by the assigned proxy", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the origin was never reached through the assigned proxy")
	}
	if !strings.Contains(string(chat.Body), "through-proxy") {
		t.Errorf("client must receive the origin's answer: %s", truncate(chat.Body))
	}
}

func copyBody(w http.ResponseWriter, r interface{ Read([]byte) (int, error) }) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			_, _ = w.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}
