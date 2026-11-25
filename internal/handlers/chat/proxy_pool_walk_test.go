package chat

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
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
	firstURL := poolWalkProxyURLOf(t, first)
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
	if got := poolWalkProxyURLOf(t, second); got == firstURL {
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
	if got := h.nextProxyURL(connData, pool); got != "" {
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
		got := h.nextProxyURL(connData, pool)
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
	got := h.nextProxyURL(other, pool)
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
			got := h.proxyEgressIsDead(context.Background(), &ConnectionData{ResolvedProxyURL: tt.url, ProxyPoolID: "p"})
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

// startMockSocks5Proxy starts a real RFC 1928 + RFC 1929 (user/pass) SOCKS5
// server on loopback that redirects all tunnelled traffic to targetServerAddr.
func startMockSocks5Proxy(t *testing.T, wantUser, wantPass, targetServerAddr string, hits *atomic.Int64) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen socks5: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			c, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				// 1. Greeting: VER(1) NMETHODS(1) METHODS(N)
				hdr := make([]byte, 2)
				if _, err := io.ReadFull(conn, hdr); err != nil || hdr[0] != 0x05 {
					return
				}
				methods := make([]byte, int(hdr[1]))
				if _, err := io.ReadFull(conn, methods); err != nil {
					return
				}
				// Select username/password auth (0x02)
				if _, err := conn.Write([]byte{0x05, 0x02}); err != nil {
					return
				}
				// 2. RFC 1929 subnegotiation: VER(0x01) ULEN UNAME PLEN PASSWD
				authHdr := make([]byte, 2)
				if _, err := io.ReadFull(conn, authHdr); err != nil || authHdr[0] != 0x01 {
					return
				}
				uname := make([]byte, int(authHdr[1]))
				if _, err := io.ReadFull(conn, uname); err != nil {
					return
				}
				plen := make([]byte, 1)
				if _, err := io.ReadFull(conn, plen); err != nil {
					return
				}
				passwd := make([]byte, int(plen[0]))
				if _, err := io.ReadFull(conn, passwd); err != nil {
					return
				}
				if string(uname) != wantUser || string(passwd) != wantPass {
					_, _ = conn.Write([]byte{0x01, 0x01})
					return
				}
				if _, err := conn.Write([]byte{0x01, 0x00}); err != nil {
					return
				}
				// 3. Request: VER(0x05) CMD(0x01) RSV(0x00) ATYP
				reqHdr := make([]byte, 4)
				if _, err := io.ReadFull(conn, reqHdr); err != nil || reqHdr[0] != 0x05 || reqHdr[1] != 0x01 {
					return
				}
				switch reqHdr[3] {
				case 0x01: // IPv4
					_, _ = io.ReadFull(conn, make([]byte, 4+2))
				case 0x03: // Domain
					dlen := make([]byte, 1)
					_, _ = io.ReadFull(conn, dlen)
					_, _ = io.ReadFull(conn, make([]byte, int(dlen[0])+2))
				case 0x04: // IPv6
					_, _ = io.ReadFull(conn, make([]byte, 16+2))
				default:
					return
				}
				hits.Add(1)
				upConn, dialErr := net.Dial("tcp", targetServerAddr)
				if dialErr != nil {
					_, _ = conn.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
					return
				}
				defer upConn.Close()
				if _, err := conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 127, 0, 0, 1, 0, 0}); err != nil {
					return
				}
				done := make(chan struct{})
				go func() {
					_, _ = io.Copy(upConn, conn)
					close(done)
				}()
				_, _ = io.Copy(conn, upConn)
				<-done
			}(c)
		}
	}()

	return "socks5://" + wantUser + ":" + wantPass + "@" + ln.Addr().String()
}

// TestWalkAcrossSeparateImportedPools_Until200OK reproduces the exact dashboard
// bulk-import topology: hundreds of separate single-URL SOCKS5 ProxyPool rows
// ("Imported <ip>:<port>") with provider-level round-robin rotation on the
// keyless conn=default OpenCode lane. A single client request must step 1-by-1
// past dead SOCKS5 sockets and 429 FreeUsageLimitError pools until a healthy
// SOCKS5 pool returns 200 OK, and park the failed pools so the next request
// skips them immediately.
func TestWalkAcrossSeparateImportedPools_Until200OK(t *testing.T) {
	resetProxyCooldownsForTest()
	t.Cleanup(resetProxyCooldownsForTest)
	t.Setenv("ALLOW_PRIVATE_NETWORKING", "true")
	t.Setenv("OPENCODE_PLAIN_HTTP", "1")

	refusalUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "7200")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"FreeUsageLimitError","message":"Free usage limit reached."}}`))
	}))
	defer refusalUpstream.Close()

	var healthyUpstreamHits atomic.Int64
	healthyUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		healthyUpstreamHits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"id\":\"gen-1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"OK_FROM_HEALTHY_PROXY\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	defer healthyUpstream.Close()

	origCfg := providers.KnownProviders["opencode"]
	patchedCfg := origCfg
	patchedCfg.BaseURL = healthyUpstream.URL + "/inference/openai/v1/chat/completions"
	providers.KnownProviders["opencode"] = patchedCfg
	t.Cleanup(func() { providers.KnownProviders["opencode"] = origCfg })

	var refused2Hits, refused3Hits, healthy4Hits, healthy5Hits atomic.Int64

	deadSocks := "socks5://u1:p1@" + closedPort(t)
	s2 := startMockSocks5Proxy(t, "u2", "p2", refusalUpstream.Listener.Addr().String(), &refused2Hits)
	s3 := startMockSocks5Proxy(t, "u3", "p3", refusalUpstream.Listener.Addr().String(), &refused3Hits)
	s4 := startMockSocks5Proxy(t, "u4", "p4", healthyUpstream.Listener.Addr().String(), &healthy4Hits)
	s5 := startMockSocks5Proxy(t, "u5", "p5", healthyUpstream.Listener.Addr().String(), &healthy5Hits)

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	dropSeededConn(t, database)

	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS proxyPools (
		id TEXT PRIMARY KEY,
		isActive INTEGER DEFAULT 1,
		testStatus TEXT,
		data TEXT NOT NULL,
		createdAt TEXT NOT NULL,
		updatedAt TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("create proxyPools: %v", err)
	}
	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS settings (id INTEGER PRIMARY KEY, data TEXT);`); err != nil {
		t.Fatalf("create settings: %v", err)
	}

	repo := db.NewRepo(database)
	for i, u := range []string{deadSocks, s2, s3, s4, s5} {
		_, err := database.Exec(`INSERT INTO proxyPools (id, isActive, testStatus, data, createdAt, updatedAt)
			VALUES (?, 1, 'unknown', ?, ?, ?)`,
			"imp-socks-"+itoa(i+1),
			`{"name":"Imported `+itoa(i+1)+`","urls":["`+u+`"],"type":"socks5"}`,
			"2026-11-17T00:00:0"+itoa(i+1)+"Z",
			"2026-11-17T00:00:0"+itoa(i+1)+"Z",
		)
		if err != nil {
			t.Fatalf("insert pool %d: %v", i+1, err)
		}
	}

	settingsJSON := `{"providerStrategies":{"opencode":{"rotateStrategy":"round-robin"}}}`
	if _, err := database.Exec(`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`, settingsJSON); err != nil {
		t.Fatalf("insert settings: %v", err)
	}

	// Reset provider cursor for deterministic 1 -> 2 -> 3 -> 4 -> 5 order.
	poolRotationCursors.Delete("opencode")

	h := NewChatHandler(repo)
	reqBody := []byte(`{"model":"mimo-v2.6-flash-free","messages":[{"role":"user","content":"Say OK"}],"stream":false}`)

	// Request 1: must step 1-by-1 past dead SOCKS5 pool 1, 429 SOCKS5 pool 2,
	// 429 SOCKS5 pool 3, and succeed on SOCKS5 pool 4 with 200 OK!
	rec1 := httptest.NewRecorder()
	if err := h.handleAccountFallback(context.Background(), rec1, "opencode", "mimo-v2.6-flash-free", "", reqBody, false, false, "/v1/chat/completions"); err != nil {
		t.Fatalf("Request 1 failed instead of walking to healthy SOCKS5 pool 4: %v", err)
	}
	if rec1.Code != http.StatusOK || !strings.Contains(rec1.Body.String(), "OK_FROM_HEALTHY_PROXY") {
		t.Fatalf("Request 1 response = %d %s, want 200 with OK_FROM_HEALTHY_PROXY", rec1.Code, rec1.Body.String())
	}
	if refused2Hits.Load() != 1 || refused3Hits.Load() != 1 {
		t.Errorf("expected SOCKS5 pools 2 and 3 to be tried once each in 1-by-1 order (no double-tap), got s2=%d s3=%d", refused2Hits.Load(), refused3Hits.Load())
	}
	if healthy4Hits.Load() != 1 {
		t.Errorf("expected healthy SOCKS5 pool 4 to serve request 1, got hits=%d", healthy4Hits.Load())
	}

	// Request 2: pools 1, 2, 3 are parked in poolCooldowns, so round-robin skips
	// them immediately and lands on healthy SOCKS5 pool 5 on try #1!
	rec2 := httptest.NewRecorder()
	if err := h.handleAccountFallback(context.Background(), rec2, "opencode", "mimo-v2.6-flash-free", "", reqBody, false, false, "/v1/chat/completions"); err != nil {
		t.Fatalf("Request 2 failed: %v", err)
	}
	if rec2.Code != http.StatusOK || !strings.Contains(rec2.Body.String(), "OK_FROM_HEALTHY_PROXY") {
		t.Fatalf("Request 2 response = %d %s, want 200 with OK_FROM_HEALTHY_PROXY", rec2.Code, rec2.Body.String())
	}
	if refused2Hits.Load() != 1 || refused3Hits.Load() != 1 {
		t.Errorf("cooled SOCKS5 pools 2 and 3 must NOT be hit on Request 2, got s2=%d s3=%d", refused2Hits.Load(), refused3Hits.Load())
	}
	if healthy5Hits.Load() != 1 {
		t.Errorf("expected healthy SOCKS5 pool 5 to serve request 2 on try #1, got s4=%d s5=%d", healthy4Hits.Load(), healthy5Hits.Load())
	}

	// Request 3: wraps around 5 -> 1,2,3 (all cooled!) -> skips straight to pool 4
	// (reusing its pooled keep-alive SOCKS5 tunnel)!
	rec3 := httptest.NewRecorder()
	if err := h.handleAccountFallback(context.Background(), rec3, "opencode", "mimo-v2.6-flash-free", "", reqBody, false, false, "/v1/chat/completions"); err != nil {
		t.Fatalf("Request 3 failed: %v", err)
	}
	if refused2Hits.Load() != 1 || refused3Hits.Load() != 1 {
		t.Errorf("cooled SOCKS5 pools 2 and 3 must still be skipped on wrap-around, got s2=%d s3=%d", refused2Hits.Load(), refused3Hits.Load())
	}
	if healthyUpstreamHits.Load() != 3 {
		t.Errorf("expected all 3 client requests to reach healthy upstream via pools 4->5->4, got %d", healthyUpstreamHits.Load())
	}
}

// poolWalkProxyURLOf resolves the proxy a client dials, or "" when it dials
// directly.
//
// Named apart from the identical helper in noproxy_dial_test.go on purpose: two
// PRs adding the same package-level test helper is a build collision, because
// nothing in `go build` sees it and nothing in either PR's own CI can see the
// other. Whichever lands second renames.
func poolWalkProxyURLOf(t *testing.T, client *http.Client) string {
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
