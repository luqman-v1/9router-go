package chat

import (
	"net/http"
	"testing"

	"9router/proxy/internal/db"
)

func TestGetClientForConnection_ProxyPool(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS proxyPools (
		id TEXT PRIMARY KEY,
		isActive INTEGER DEFAULT 1,
		testStatus TEXT,
		data TEXT NOT NULL,
		createdAt TEXT NOT NULL,
		updatedAt TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("failed to create proxyPools table: %v", err)
	}

	repo := db.NewRepo(database)
	pool, err := repo.InsertProxyPool(db.ProxyPoolData{
		Name:     "sg-proxy",
		ProxyURL: "http://user:pass@proxy.example.com:8080",
		Type:     "http",
	})
	if err != nil {
		t.Fatalf("failed to insert proxy pool: %v", err)
	}

	poolID := pool["id"].(string)

	h := &ChatHandler{
		Client: &http.Client{},
		Repo:   repo,
	}

	connData := &ConnectionData{
		ProxyPoolID: poolID,
	}

	client, err := h.GetClientForConnection(connData)
	if err != nil {
		t.Fatalf("client for connection: %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if client == h.Client {
		t.Fatal("expected new client with custom proxy transport, got default client")
	}

	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport == nil {
		t.Fatal("expected *http.Transport")
	}

	req, _ := http.NewRequest("GET", "https://api.openai.com/v1/models", nil)
	proxyURL, err := transport.Proxy(req)
	if err != nil {
		t.Fatalf("proxy resolve error: %v", err)
	}
	if proxyURL == nil || proxyURL.String() != "http://user:pass@proxy.example.com:8080" {
		t.Errorf("expected proxy URL http://user:pass@proxy.example.com:8080, got %v", proxyURL)
	}
}

// A Vercel relay pasted into a pool declared "http" must never be dialed as a
// CONNECT proxy: that answers "malformed HTTP status code". The plain client is
// returned, and getProviderConfig routes the request through the relay via its
// x-relay-target / x-relay-path headers instead.
func TestGetClientForConnection_RelayPoolDeclaredAsHTTP(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS proxyPools (
		id TEXT PRIMARY KEY,
		isActive INTEGER DEFAULT 1,
		testStatus TEXT,
		data TEXT NOT NULL,
		createdAt TEXT NOT NULL,
		updatedAt TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("failed to create proxyPools table: %v", err)
	}

	repo := db.NewRepo(database)
	pool, err := repo.InsertProxyPool(db.ProxyPoolData{
		Name:        "vercel-relay",
		ProxyURL:    "https://relay-deploy.vercel.app",
		Type:        "http",
		StrictProxy: true,
	})
	if err != nil {
		t.Fatalf("failed to insert proxy pool: %v", err)
	}

	plain := &http.Client{}
	h := &ChatHandler{Client: plain, Repo: repo}

	client, err := h.GetClientForConnection(&ConnectionData{ProxyPoolID: pool["id"].(string)})
	if err != nil {
		t.Fatalf("client for connection: %v", err)
	}
	if client != plain {
		t.Error("a relay pool must use the plain client, not an http.ProxyURL tunnel to the relay")
	}
}

// End to end for the reported failure: a no-auth provider (opencode) with the
// Vercel relay assigned as its pool. Before the fix the relay was dialed with
// http.ProxyURL and answered "malformed HTTP status code"; now the request must
// leave through the relay via x-relay-target / x-relay-path instead.
func TestRelayPoolAssignedToNoAuthProviderUsesRelayHeaders(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS proxyPools (
		id TEXT PRIMARY KEY,
		isActive INTEGER DEFAULT 1,
		testStatus TEXT,
		data TEXT NOT NULL,
		createdAt TEXT NOT NULL,
		updatedAt TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("failed to create proxyPools table: %v", err)
	}
	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS settings (
		id INTEGER PRIMARY KEY,
		data TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("failed to create settings table: %v", err)
	}

	repo := db.NewRepo(database)
	pool, err := repo.InsertProxyPool(db.ProxyPoolData{
		Name:        "proxy",
		ProxyURL:    "https://relay-deploy.vercel.app",
		Type:        "http",
		StrictProxy: true,
	})
	if err != nil {
		t.Fatalf("failed to insert proxy pool: %v", err)
	}
	poolID := pool["id"].(string)

	settingsJSON := `{"providerStrategies":{"opencode":{"proxyPoolId":"` + poolID + `"}}}`
	if _, err := database.Exec(`INSERT OR REPLACE INTO settings (id, data) VALUES (1, ?)`, settingsJSON); err != nil {
		t.Fatalf("failed to seed settings: %v", err)
	}

	h := NewChatHandler(repo)

	_, connData, err := h.getBestConnection("opencode", "", nil, "mimo-v2.5-free")
	if err != nil {
		t.Fatalf("getBestConnection: %v", err)
	}
	if connData.ProxyPoolID != poolID {
		t.Fatalf("expected the assigned pool %s on the no-auth connection, got %q", poolID, connData.ProxyPoolID)
	}

	client, err := h.GetClientForConnection(connData)
	if err != nil {
		t.Fatalf("GetClientForConnection: %v", err)
	}
	if client != h.Client {
		t.Error("relay pool must use the plain client, not an http.ProxyURL tunnel to the relay")
	}

	cfg, err := h.getProviderConfig("opencode", connData)
	if err != nil {
		t.Fatalf("getProviderConfig: %v", err)
	}
	if want := "https://relay-deploy.vercel.app"; cfg.BaseURL != want {
		t.Errorf("BaseURL = %q, want the relay endpoint %q", cfg.BaseURL, want)
	}
	if got := cfg.StaticHeaders["x-relay-target"]; got != "https://opencode.ai" {
		t.Errorf("x-relay-target = %q, want https://opencode.ai", got)
	}
	if got := cfg.StaticHeaders["x-relay-path"]; got != "/inference/openai/v1/chat/completions" {
		t.Errorf("x-relay-path = %q, want /inference/openai/v1/chat/completions", got)
	}
}
