package chat

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/db"
	internalproxy "9router/proxy/internal/proxy"
)

// A proxy pool whose URL points at a closed port: the proxy cannot serve a
// request, so anything the gateway answers came out without it. These tests
// stand in for the real leak — the upstream answering while the operator
// believes traffic left through the pool.
type strictProxyFixture struct {
	repo     *db.Repo
	upstream *httptest.Server
	cleanup  func()
}

// deadProxyURL is a URL nothing listens on: the pool is configured and active,
// but every attempt through it fails at connect time.
const deadProxyURL = "http://127.0.0.1:1"

func newStrictProxyFixture(t *testing.T) *strictProxyFixture {
	t.Helper()

	database, cleanupDB := setupChatTestDB(t)
	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS proxyPools (
		id TEXT PRIMARY KEY,
		isActive INTEGER DEFAULT 1,
		testStatus TEXT,
		data TEXT NOT NULL,
		createdAt TEXT NOT NULL,
		updatedAt TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("create proxyPools table: %v", err)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))

	return &strictProxyFixture{
		repo:     db.NewRepo(database),
		upstream: upstream,
		cleanup: func() {
			upstream.Close()
			cleanupDB()
		},
	}
}

func (f *strictProxyFixture) handler() *ChatHandler {
	return &ChatHandler{Client: &http.Client{}, Repo: f.repo}
}

func (f *strictProxyFixture) addPool(t *testing.T, strict bool) string {
	t.Helper()
	pool, err := f.repo.InsertProxyPool(db.ProxyPoolData{
		Name:        "pool",
		ProxyURL:    deadProxyURL,
		Type:        "http",
		StrictProxy: strict,
	})
	if err != nil {
		t.Fatalf("insert proxy pool: %v", err)
	}
	return pool["id"].(string)
}

// A connection bound to a dead pool must fail the request. Answering it would
// publish the operator's egress IP to the upstream — the one outcome the pool
// assignment exists to prevent — while the dashboard still shows the connection
// as proxied.
func TestStrictProxyPoolRefusesToLeakDirectTraffic(t *testing.T) {
	f := newStrictProxyFixture(t)
	defer f.cleanup()

	h := f.handler()
	client, err := h.GetClientForConnection(&ConnectionData{ProxyPoolID: f.addPool(t, true)})
	if err != nil {
		t.Fatalf("a configured strict pool must resolve a client, got %v", err)
	}

	resp, err := client.Get(f.upstream.URL)
	if err == nil {
		defer resp.Body.Close()
		t.Fatalf("strictProxy must fail the request when the proxy is dead, got %d", resp.StatusCode)
	}
}

// Same rule for the legacy per-connection proxy, which is a separate code path
// from the pool.
func TestStrictProxyConnectionRefusesToLeakDirectTraffic(t *testing.T) {
	f := newStrictProxyFixture(t)
	defer f.cleanup()

	h := f.handler()
	client, err := h.GetClientForConnection(&ConnectionData{
		ProviderSpecificData: map[string]any{
			"connectionProxyEnabled": true,
			"connectionProxyUrl":     deadProxyURL,
			"strictProxy":            true,
		},
	})
	if err != nil {
		t.Fatalf("a configured strict proxy must resolve a client, got %v", err)
	}

	resp, err := client.Get(f.upstream.URL)
	if err == nil {
		defer resp.Body.Close()
		t.Fatalf("strictProxy must fail the request when the proxy is dead, got %d", resp.StatusCode)
	}
}

// Without strictProxy a dead proxy may fall back — but the request still must not
// be served, because a transport-level fallback is exactly how a request gets
// completed from the wrong IP. This pins the difference so the strict path is
// not "fixed" by deleting the fallback entirely.
func TestNonStrictProxyStillDoesNotServeFromTheRealIP(t *testing.T) {
	f := newStrictProxyFixture(t)
	defer f.cleanup()

	h := f.handler()
	client, err := h.GetClientForConnection(&ConnectionData{ProxyPoolID: f.addPool(t, false)})
	if err != nil {
		t.Fatalf("non-strict pool must resolve a client, got %v", err)
	}

	resp, err := client.Get(f.upstream.URL)
	if err != nil {
		return // failing is correct
	}
	defer resp.Body.Close()
	t.Fatalf("a dead proxy must not produce a served answer, got %d", resp.StatusCode)
}

// Relay pools (vercel/cloudflare/deno) are not dialed as HTTP proxies, so the
// noProxy bypass must not strip their x-relay routing headers. An operator
// listing the upstream host in noProxy expects the direct path there.
func TestRelayPoolKeepsRelayRoutingHeaders(t *testing.T) {
	f := newStrictProxyFixture(t)
	defer f.cleanup()

	pool, err := f.repo.InsertProxyPool(db.ProxyPoolData{
		Name:     "relay",
		ProxyURL: "https://relay.example",
		Type:     "vercel",
	})
	if err != nil {
		t.Fatalf("insert relay pool: %v", err)
	}

	h := f.handler()
	cfg, err := h.getProviderConfig("openai", &ConnectionData{
		ProxyPoolID: pool["id"].(string),
		BaseURL:     "https://api.openai.com/v1/chat/completions",
	})
	if err != nil {
		t.Fatalf("provider config for relay pool: %v", err)
	}
	if cfg.StaticHeaders["x-relay-target"] != "https://api.openai.com" {
		t.Errorf("relay pool must stamp x-relay-target, got %q", cfg.StaticHeaders["x-relay-target"])
	}
	if cfg.BaseURL != "https://relay.example" {
		t.Errorf("relay pool must rewrite BaseURL to the relay host, got %q", cfg.BaseURL)
	}
}

// A host the operator listed in the pool's noProxy must not be relayed or
// proxied: that list is how an operator carves out direct egress.
func TestNoProxyBypassesRelayForListedHost(t *testing.T) {
	f := newStrictProxyFixture(t)
	defer f.cleanup()

	pool, err := f.repo.InsertProxyPool(db.ProxyPoolData{
		Name:     "relay-bypass",
		ProxyURL: "https://relay.example",
		Type:     "vercel",
		NoProxy:  "api.openai.com",
	})
	if err != nil {
		t.Fatalf("insert relay pool: %v", err)
	}

	h := f.handler()
	cfg, err := h.getProviderConfig("openai", &ConnectionData{
		ProxyPoolID: pool["id"].(string),
		BaseURL:     "https://api.openai.com/v1/chat/completions",
	})
	if err != nil {
		t.Fatalf("provider config for noProxy-listed host: %v", err)
	}
	if _, relayed := cfg.StaticHeaders["x-relay-target"]; relayed {
		t.Error("a noProxy-listed host must go direct, not through the relay")
	}
	if cfg.BaseURL != "https://api.openai.com/v1/chat/completions" {
		t.Errorf("noProxy bypass must keep the upstream URL, got %q", cfg.BaseURL)
	}
}

func TestShouldBypassNoProxyCoversEveryRelayHostPattern(t *testing.T) {
	tests := []struct {
		target string
		list   string
		want   bool
	}{
		{"https://api.openai.com/v1/chat/completions", "", false},
		{"https://api.openai.com/v1/chat/completions", "api.openai.com", true},
		{"https://api.openai.com/v1/chat/completions", "openai.com", true},
		{"https://api.openai.com/v1/chat/completions", ".openai.com", true},
		{"https://sub.openai.com/v1", ".openai.com", true},
		{"https://api.openai.com/v1", "anthropic.com,openai.com", true},
		{"https://api.anthropic.com/v1", "anthropic.com,openai.com", true},
		{"https://api.openai.com/v1", "notopenai.com", false},
		{"https://api.openai.com/v1", "*", true},
	}
	for _, tt := range tests {
		if got := internalproxy.ShouldBypassNoProxy(tt.target, tt.list); got != tt.want {
			t.Errorf("ShouldBypassNoProxy(%q, %q) = %v, want %v", tt.target, tt.list, got, tt.want)
		}
	}
}
