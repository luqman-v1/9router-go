package chat

import (
	"net/http"
	"net/url"
	"testing"

	"9router/proxy/internal/db"
)

// newNoProxyRepo builds a handler whose pool table holds one pool per case, so
// a pool typed "socks5" reaches the resolver exactly as it does in production.
func newNoProxyRepo(t *testing.T, pools []struct{ id, typ, proxyURL, noProxy string }) (*ChatHandler, *http.Client, func()) {
	t.Helper()

	database, cleanup := setupChatTestDB(t)
	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS proxyPools (
		id TEXT PRIMARY KEY,
		isActive INTEGER DEFAULT 1,
		testStatus TEXT,
		data TEXT NOT NULL,
		createdAt TEXT NOT NULL,
		updatedAt TEXT NOT NULL
	)`); err != nil {
		cleanup()
		t.Fatalf("create proxyPools: %v", err)
	}

	for _, p := range pools {
		data := `{"name":"` + p.typ + `","urls":["` + p.proxyURL + `"],"type":"` + p.typ + `"`
		if p.noProxy != "" {
			data += `,"noProxy":"` + p.noProxy + `"`
		}
		data += `}`
		if _, err := database.Exec(`INSERT INTO proxyPools (id, isActive, testStatus, data, createdAt, updatedAt)
			VALUES (?, 1, 'unknown', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
			p.id, data); err != nil {
			cleanup()
			t.Fatalf("insert pool %s: %v", p.id, err)
		}
	}

	direct := &http.Client{Transport: &http.Transport{}}
	h := &ChatHandler{Repo: db.NewRepo(database), Client: direct}
	return h, direct, cleanup
}

// proxyURLOf resolves the proxy a client dials, or "" when it dials directly.
func proxyURLOf(t *testing.T, client *http.Client) string {
	t.Helper()
	tr, ok := client.Transport.(*http.Transport)
	if !ok || tr.Proxy == nil {
		return ""
	}
	u, err := tr.Proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: "api.example.invalid"}})
	if err != nil || u == nil {
		return ""
	}
	return u.String()
}

// A dialable pool's noProxy list must be honoured on the dial path. It was read
// only for edge relays (connections.go resolveEdgeRelay), so the field was
// stored, rendered in the dashboard, and silently ignored for CONNECT and SOCKS
// pools: a target the operator explicitly excluded still went out through the
// proxy.
func TestDialablePoolNoProxyIsHonoured(t *testing.T) {
	h, direct, cleanup := newNoProxyRepo(t, []struct{ id, typ, proxyURL, noProxy string }{
		{"pool-noproxy", "socks5", "socks5://user:pass@127.0.0.1:1080", "api.skipped.example"},
	})
	defer cleanup()

	tests := []struct {
		name       string
		baseURL    string
		wantDirect bool
	}{
		{
			name:       "a target the list names dials direct",
			baseURL:    "https://api.skipped.example/v1",
			wantDirect: true,
		},
		{
			name:       "a subdomain of a listed host dials direct",
			baseURL:    "https://eu.api.skipped.example/v1",
			wantDirect: true,
		},
		{
			name:       "a target outside the list keeps the proxy",
			baseURL:    "https://api.proxied.example/v1",
			wantDirect: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := h.getClientForConnection(&ConnectionData{
				ProxyPoolID: "pool-noproxy",
				BaseURL:     tt.baseURL,
			})
			if err != nil {
				t.Fatalf("resolve client: %v", err)
			}
			if gotDirect := client == direct; gotDirect != tt.wantDirect {
				t.Fatalf("direct = %v, want %v (proxy url %q)", gotDirect, tt.wantDirect, proxyURLOf(t, client))
			}
		})
	}
}

// A pool with no noProxy list keeps proxying everything — the empty list must
// not be read as "bypass all".
func TestEmptyNoProxyStillProxies(t *testing.T) {
	h, direct, cleanup := newNoProxyRepo(t, []struct{ id, typ, proxyURL, noProxy string }{
		{"pool-plain", "socks5", "socks5://user:pass@127.0.0.1:1080", ""},
	})
	defer cleanup()

	client, err := h.getClientForConnection(&ConnectionData{
		ProxyPoolID: "pool-plain",
		BaseURL:     "https://api.example.com/v1",
	})
	if err != nil {
		t.Fatalf("resolve client: %v", err)
	}
	if client == direct {
		t.Fatal("a pool with no noProxy list must still proxy")
	}
	if got := proxyURLOf(t, client); got != "socks5://user:pass@127.0.0.1:1080" {
		t.Errorf("proxy url = %q, want the pool's socks5 url", got)
	}
}
