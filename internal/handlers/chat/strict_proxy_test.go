package chat

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/db"
	internalproxy "9router/proxy/internal/proxy"
)

// setupProxyPools gives the handler a repo that can store proxy pools and
// returns it.
func setupProxyPools(t *testing.T) *db.Repo {
	t.Helper()
	database, cleanup := setupChatTestDB(t)
	t.Cleanup(cleanup)

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
	return db.NewRepo(database)
}

// directCounter answers 200 and counts the traffic that reached it without a
// proxy. A strict connection that answers here has published the operator's
// real IP, which is the whole thing the setting exists to prevent.
func directCounter(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// TestStrictConnectionRefusesUnparseableProxyURL is the #4333 leak at the
// legacy-connection path: a strict connection whose proxy url the transport
// cannot dial used to log and hand back the direct client, so the request left
// over the real IP while the connection still looked proxied.
func TestStrictConnectionRefusesUnparseableProxyURL(t *testing.T) {
	repo := setupProxyPools(t)
	h := &ChatHandler{Client: &http.Client{}, Repo: repo}

	// "://nonsense" carries no scheme or host, so url.Parse cannot use it as a
	// proxy target.
	connData := &ConnectionData{
		ConnectionProxyEnabled: true,
		ConnectionProxyURL:     "://nonsense",
		StrictProxy:            true,
	}

	client, err := h.GetClientForConnection(connData)
	if err == nil {
		t.Fatalf("a strict connection with an unusable proxy url must fail, got client %v", client)
	}
	if !isStrictRefusal(err) {
		t.Errorf("error = %v, want it to report the strict-proxy refusal", err)
	}
}

// TestNonStrictConnectionStillFallsBackOnBadProxyURL is what the strict branch
// must not take away: an ordinary connection whose proxy url is unusable still
// degrades to the shared direct client instead of failing.
func TestNonStrictConnectionStillFallsBackOnBadProxyURL(t *testing.T) {
	repo := setupProxyPools(t)
	h := &ChatHandler{Client: &http.Client{}, Repo: repo}

	connData := &ConnectionData{
		ConnectionProxyEnabled: true,
		ConnectionProxyURL:     "://nonsense",
	}

	client, err := h.GetClientForConnection(connData)
	if err != nil {
		t.Fatalf("a non-strict connection must still fall back: %v", err)
	}
	if client != h.Client {
		t.Error("a non-strict connection with an unusable url must get the shared direct client")
	}
}

// TestStrictProxyFlagAloneDoesNotBlockDirectUpstream is the upstream nuance
// that must survive the port: the Qoder executor sets strictProxy with nothing
// proxied at all, meaning "never replay this request directly". Blocking that
// would take a provider that works today offline, so a bare flag may not turn
// an ordinary direct connection into an error.
func TestStrictProxyFlagAloneDoesNotBlockDirectUpstream(t *testing.T) {
	repo := setupProxyPools(t)
	h := &ChatHandler{Client: &http.Client{}, Repo: repo}

	connData := &ConnectionData{StrictProxy: true}

	client, err := h.GetClientForConnection(connData)
	if err != nil {
		t.Fatalf("strict with no proxy configured must keep working direct: %v", err)
	}
	if client != h.Client {
		t.Error("a strict flag alone must not change which client is used")
	}
}

// TestStrictConnectionRefusesWhenNoProxyResolves covers the other strict
// refusal: a connection that enables a proxy but resolves no usable URL — an
// empty url, or one only present in providerSpecificData — used to be treated
// as "no proxy" and answered from the real IP.
func TestStrictConnectionRefusesWhenNoProxyResolves(t *testing.T) {
	cases := []struct {
		name string
		conn ConnectionData
	}{
		{
			name: "proxy enabled with no url at all",
			conn: ConnectionData{ConnectionProxyEnabled: true, StrictProxy: true},
		},
		{
			name: "proxy url present but not enabled",
			conn: ConnectionData{ConnectionProxyURL: "http://127.0.0.1:8888", StrictProxy: true},
		},
		{
			name: "strict flag only in provider specific data, proxy enabled with no url",
			conn: ConnectionData{
				ProviderSpecificData: map[string]any{
					"connectionProxyEnabled": true,
					"strictProxy":            true,
				},
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			repo := setupProxyPools(t)
			h := &ChatHandler{Client: &http.Client{}, Repo: repo}

			connData := tt.conn
			client, err := h.GetClientForConnection(&connData)
			if err == nil {
				t.Fatalf("a strict connection with no resolvable proxy must fail, got client %v", client)
			}
			if !isStrictRefusal(err) {
				t.Errorf("error = %v, want it to report the strict-proxy refusal", err)
			}
		})
	}
}

// TestStrictPoolTrafficNeverReachesTheProviderDirectly is the end-to-end half:
// a connection bound to a strict pool, pointed at a dead proxy, must not have
// its request answered by the provider over the real IP.
func TestStrictPoolTrafficNeverReachesTheProviderDirectly(t *testing.T) {
	repo := setupProxyPools(t)
	upstream, hits := directCounter(t)

	pool, err := repo.InsertProxyPool(db.ProxyPoolData{
		Name: "dead-strict", ProxyURL: "http://127.0.0.1:1", Type: "http", StrictProxy: true,
	})
	if err != nil {
		t.Fatalf("insert proxy pool: %v", err)
	}
	poolID, _ := pool["id"].(string)

	h := &ChatHandler{Client: &http.Client{}, Repo: repo}
	client, err := h.GetClientForConnection(&ConnectionData{ProxyPoolID: poolID})
	if err != nil {
		t.Fatalf("a usable strict pool must still hand out a client: %v", err)
	}
	if client == h.Client {
		t.Fatal("a pool-bound connection must not get the shared direct client")
	}

	// The provider answers only when something dials it without a proxy.
	if _, err := internalproxy.DoRequest(t.Context(), client, http.MethodPost, upstream.URL, nil, []byte(`{}`)); err == nil {
		t.Fatal("the strict pool's dead proxy must fail the request")
	}
	if *hits != 0 {
		t.Errorf("the provider answered %d times: strict traffic escaped over the real IP", *hits)
	}
}

// TestStrictAndLaxPoolsDoNotShareACachedClient guards the cache. Proxy clients
// are pooled per URL, so a strict pool and an ordinary one pointing at the
// same proxy must not share the instance — otherwise the second caller
// inherits the first one's decision.
func TestStrictAndLaxPoolsDoNotShareACachedClient(t *testing.T) {
	repo := setupProxyPools(t)
	strictPool, err := repo.InsertProxyPool(db.ProxyPoolData{
		Name: "shared-url-strict", ProxyURL: "http://127.0.0.1:8888", Type: "http", StrictProxy: true,
	})
	if err != nil {
		t.Fatalf("insert strict pool: %v", err)
	}
	laxPool, err := repo.InsertProxyPool(db.ProxyPoolData{
		Name: "shared-url-lax", ProxyURL: "http://127.0.0.1:8888", Type: "http", StrictProxy: false,
	})
	if err != nil {
		t.Fatalf("insert lax pool: %v", err)
	}

	h := &ChatHandler{Client: &http.Client{}, Repo: repo}
	strictClient, err := h.GetClientForConnection(&ConnectionData{ProxyPoolID: strictPool["id"].(string)})
	if err != nil {
		t.Fatalf("strict client: %v", err)
	}
	laxClient, err := h.GetClientForConnection(&ConnectionData{ProxyPoolID: laxPool["id"].(string)})
	if err != nil {
		t.Fatalf("lax client: %v", err)
	}

	if internalproxy.ForbidsDirectReplay(strictClient) == internalproxy.ForbidsDirectReplay(laxClient) {
		t.Error("a strict pool and a lax pool on the same url must not share a cached client")
	}
}

// isStrictRefusal reports whether err is the strict-proxy refusal. It goes
// through errors.Is so the test pins the sentinel rather than a message.
func isStrictRefusal(err error) bool {
	return errors.Is(err, internalproxy.ErrStrictProxyRequired)
}