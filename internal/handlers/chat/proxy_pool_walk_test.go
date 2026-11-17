package chat

import (
	"net"
	"net/http"
	"net/url"
	"testing"

	"9router/proxy/internal/db"
)

// newWalkRepo builds a handler with one pool of the given URLs and a connection
// bound to it — the shape a hundred-proxy round-robin pool actually has.
func newWalkRepo(t *testing.T, poolID string, urls []string) (*ChatHandler, func()) {
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

	data := `{"name":"pool","urls":[`
	for i, u := range urls {
		if i > 0 {
			data += ","
		}
		data += `"` + u + `"`
	}
	data += `],"type":"socks5","strategy":"round-robin"}`
	if _, err := database.Exec(`INSERT INTO proxyPools (id, isActive, testStatus, data, createdAt, updatedAt)
		VALUES (?, 1, 'unknown', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		poolID, data); err != nil {
		cleanup()
		t.Fatalf("insert pool: %v", err)
	}

	return &ChatHandler{Repo: db.NewRepo(database), Client: &http.Client{}}, cleanup
}

// The core of the bug: a pool is many egresses behind ONE connection. Resolving
// once per turn meant a refused proxy was never walked past, so a hundred-proxy
// pool produced one attempt and the turn failed.
func TestResolveWalksPastARefusedProxy(t *testing.T) {
	urls := []string{
		"socks5://u:p@10.0.0.1:1080",
		"socks5://u:p@10.0.0.2:1080",
		"socks5://u:p@10.0.0.3:1080",
	}
	h, cleanup := newWalkRepo(t, "walk-pool-1", urls)
	defer cleanup()

	connData := &ConnectionData{ProxyPoolID: "walk-pool-1"}
	first, err := h.getClientForConnection(connData)
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	firstURL := proxyURLOf(t, first)
	if firstURL == "" {
		t.Fatal("first resolve dialled directly, want a proxy")
	}

	refuseProxy(connData)

	second, err := h.getClientForConnection(connData)
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	// NextURL pre-increments, so which URL comes first is not fixed; what
	// matters is that the refused one is not handed back.
	if got := proxyURLOf(t, second); got == firstURL {
		t.Errorf("after one refusal the resolver dialled %q again, want a different proxy", got)
	}
}

// With every proxy refused the resolver reports exhaustion rather than silently
// reusing a spent one. The caller turns that into the last upstream refusal, so
// the operator sees what the gate said.
func TestResolveReportsExhaustionWhenEveryProxyIsRefused(t *testing.T) {
	urls := []string{
		"socks5://u:p@10.0.0.1:1080",
		"socks5://u:p@10.0.0.2:1080",
	}
	h, cleanup := newWalkRepo(t, "walk-pool-2", urls)
	defer cleanup()

	connData := &ConnectionData{ProxyPoolID: "walk-pool-2"}
	for range urls {
		if _, err := h.getClientForConnection(connData); err != nil {
			t.Fatalf("resolve: %v", err)
		}
		refuseProxy(connData)
	}
	// Every URL is now refused, so a further selection has nothing to offer.

	pool, err := h.Repo.GetProxyPool("walk-pool-2")
	if err != nil {
		t.Fatalf("read pool: %v", err)
	}
	if got := nextProxyURL(connData, pool); got != "" {
		t.Errorf("nextProxyURL() = %q, want \"\" once every proxy is spent", got)
	}
}

// A pool with no refused proxies keeps the plain rotation, so a caller that
// never records a refusal sees exactly the old behaviour.
func TestResolveWithoutRefusalsIsPlainRotation(t *testing.T) {
	urls := []string{
		"socks5://u:p@10.0.0.1:1080",
		"socks5://u:p@10.0.0.2:1080",
	}
	h, cleanup := newWalkRepo(t, "walk-pool-3", urls)
	defer cleanup()

	pool, err := h.Repo.GetProxyPool("walk-pool-3")
	if err != nil {
		t.Fatalf("read pool: %v", err)
	}
	connData := &ConnectionData{ProxyPoolID: "walk-pool-3"}

	allowed := map[string]bool{}
	for _, u := range urls {
		allowed[u] = true
	}

	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		got := nextProxyURL(connData, pool)
		if got == "" {
			t.Fatal("nextProxyURL() = \"\" with no refusals recorded")
		}
		if !allowed[got] {
			t.Fatalf("nextProxyURL() = %q, which is not in the pool", got)
		}
		seen[got] = true
	}
	if len(seen) != 2 {
		t.Errorf("walked %d distinct proxies over 4 draws, want both", len(seen))
	}
}

// Refusals are tracked per pool, so one pool's spent proxies never affect
// another pool's selection.
func TestRefusalsAreScopedPerPool(t *testing.T) {
	urls := []string{
		"socks5://u:p@10.0.0.1:1080",
		"socks5://u:p@10.0.0.2:1080",
	}
	h, cleanup := newWalkRepo(t, "walk-pool-4", urls)
	defer cleanup()

	connData := &ConnectionData{ProxyPoolID: "walk-pool-4"}
	if _, err := h.getClientForConnection(connData); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	refuseProxy(connData)

	// A different pool id must not see the refusal.
	other := &ConnectionData{ProxyPoolID: "other-pool", RefusedProxies: connData.RefusedProxies}
	pool, err := h.Repo.GetProxyPool("walk-pool-4")
	if err != nil {
		t.Fatalf("read pool: %v", err)
	}
	got := nextProxyURL(other, pool)
	if got == "" {
		t.Error("a refusal in one pool must not exhaust another pool")
	}
}

// A dead proxy socket is an egress failure, not an upstream one. The 429 path was
// already walked; this covers the other shape, where the request never reached
// the provider at all and the error carries no status code.
//
// The distinction matters because both look identical in the log ("Post ...:
// <error>" with status 0), but one is fixed by rotating and the other is not.
// proxyEgressIsDead tells them apart by dialling the proxy's own address: a
// socket that refuses that would refuse any target.
func TestProxyEgressIsDeadDistinguishesDeadSocketFromLiveProxy(t *testing.T) {
	// A listen-then-close listener gives a port that is open at test time and
	// closed afterwards, so the probe sees a refused connection without
	// depending on any particular unroutable address.
	closed := closedPort(t)

	tests := []struct {
		name string
		url  string
		want bool
	}{
		{name: "closed port is dead", url: "socks5://u:p@" + closed, want: true},
		{name: "empty url is not a dead egress", url: "", want: false},
		{name: "unparseable url is not a dead egress", url: "://nonsense", want: false},
	}

	h := &ChatHandler{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := h.proxyEgressIsDead(&ConnectionData{ResolvedProxyURL: tt.url, ProxyPoolID: "p"})
			if got != tt.want {
				t.Errorf("proxyEgressIsDead(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

// closedPort returns host:port that nothing is listening on.
func closedPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return addr
}

// Credentials must never reach a log line or a label.
func TestRedactProxyURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "userinfo removed", in: "socks5://ux24Mhpm:OWlehFA0nxoA@103.17.36.106:33951", want: "socks5://***@103.17.36.106:33951"},
		{name: "username only removed", in: "socks5://someuser@proxy.example:1080", want: "socks5://***@proxy.example:1080"},
		{name: "no credentials untouched", in: "http://proxy.example:8080", want: "http://proxy.example:8080"},
		{name: "empty stays empty", in: "   ", want: ""},
		{name: "no scheme passes through", in: "proxy.example:8080", want: "proxy.example:8080"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := redactProxyURL(tt.in); got != tt.want {
				t.Errorf("redactProxyURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
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
