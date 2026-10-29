package chat

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

// An edge relay pool is a transport, not a rewrite.
//
// It used to be applied by rewriting BaseURL to the relay host and stamping
// x-relay-target / x-relay-path into StaticHeaders. That made relay support a
// per-provider convention: every executor that built its own request had to
// re-apply it by hand, and the ones that did not leaked the operator's real IP
// or sent a provider path to the relay's root (trae, zed, windsurf, embeddings).
//
// The contract now: BaseURL stays the real provider endpoint, RelayURL names the
// relay, and the connection's client moves the dial. These tests pin that split.

// newRelayPool inserts a pool and returns its id. The chat test schema has no
// proxyPools table, so the helper creates it: relay routing is the subject here,
// not schema bootstrap.
func newRelayPool(t *testing.T, database *sql.DB, proxyURL, poolType string) string {
	t.Helper()
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
	repo := db.NewRepo(database)
	pool, err := repo.InsertProxyPool(db.ProxyPoolData{
		Name:     "relay-pool",
		ProxyURL: proxyURL,
		Type:     poolType,
	})
	if err != nil {
		t.Fatalf("failed to insert proxy pool: %v", err)
	}
	return pool["id"].(string)
}

func TestGetProviderConfig_EdgeRelayKeepsTheProviderEndpoint(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	poolID := newRelayPool(t, database, "https://my-relay.vercel.app", "vercel")

	h := &ChatHandler{Repo: repo}
	cfg, err := h.GetProviderConfig("openai", &ConnectionData{ProxyPoolID: poolID})
	if err != nil {
		t.Fatalf("GetProviderConfig failed: %v", err)
	}

	if cfg.BaseURL != "https://api.openai.com/v1/chat/completions" {
		t.Errorf("BaseURL = %q, want the real provider endpoint: a rewrite makes every executor's own URL relative to the relay", cfg.BaseURL)
	}
	if cfg.RelayURL != "https://my-relay.vercel.app" {
		t.Errorf("RelayURL = %q, want the assigned relay", cfg.RelayURL)
	}
	// The relay headers belong to the wire, not to the provider config: stamping
	// them here is what forced each executor to know about the relay.
	if _, ok := cfg.StaticHeaders["x-relay-target"]; ok {
		t.Error("StaticHeaders carries x-relay-target; the relay is the transport's job")
	}

	// The registry entry must not be mutated by any of this.
	if orig := providers.KnownProviders["openai"]; orig.BaseURL == "https://my-relay.vercel.app" {
		t.Error("KnownProviders[openai] was mutated")
	}
}

// noProxy still bypasses the relay entirely.
func TestGetProviderConfig_EdgeRelay_NoProxyBypass(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
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
	pool, err := repo.InsertProxyPool(db.ProxyPoolData{
		Name:     "vercel-relay",
		ProxyURL: "https://my-relay.vercel.app",
		Type:     "vercel",
		NoProxy:  "api.openai.com",
	})
	if err != nil {
		t.Fatalf("failed to insert proxy pool: %v", err)
	}
	poolID := pool["id"].(string)

	h := &ChatHandler{Repo: repo}
	cfg, err := h.GetProviderConfig("openai", &ConnectionData{ProxyPoolID: poolID})
	if err != nil {
		t.Fatalf("GetProviderConfig failed: %v", err)
	}
	if cfg.BaseURL != "https://api.openai.com/v1/chat/completions" {
		t.Errorf("BaseURL = %q, want the provider endpoint", cfg.BaseURL)
	}
	if cfg.RelayURL != "" {
		t.Errorf("RelayURL = %q, want empty for a noProxy bypass", cfg.RelayURL)
	}
}

// The relay client moves the dial and names the URL the caller built. This is
// the half that makes the fix general: nothing in the provider path had to
// cooperate for the request to leave through the relay.
func TestRelayClient_MovesTheDialAndNamesTheRealURL(t *testing.T) {
	var gotTarget, gotPath, gotHost, gotMethod string
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTarget = r.Header.Get("x-relay-target")
		gotPath = r.Header.Get("x-relay-path")
		gotHost = r.Host
		gotMethod = r.Method
		w.WriteHeader(http.StatusOK)
	}))
	defer relay.Close()

	h := &ChatHandler{Client: &http.Client{}}
	client := h.relayClient(relay.URL)

	// A URL an executor built itself: a provider lane path plus a query the
	// signature depends on.
	req, err := http.NewRequest(http.MethodPost, "https://api3.qoder.sh/algo/api/v2/service/pro/sse/agent_chat_generation?Encode=1", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("relayed request: %v", err)
	}
	defer resp.Body.Close()

	if gotHost == "api3.qoder.sh" {
		t.Error("the request was dialled at the provider, not the relay")
	}
	if gotTarget != "https://api3.qoder.sh" {
		t.Errorf("x-relay-target = %q, want the provider origin", gotTarget)
	}
	if want := "/algo/api/v2/service/pro/sse/agent_chat_generation?Encode=1"; gotPath != want {
		t.Errorf("x-relay-path = %q, want %q (the executor's own path and query must survive)", gotPath, want)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
}

// An executor that already routed itself keeps its own target: opencode-zen
// picks a lane path per model, and re-stamping it would overwrite that choice.
func TestRelayTransport_KeepsAnExecutorChosenTarget(t *testing.T) {
	var gotPath string
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.Header.Get("x-relay-path")
		w.WriteHeader(http.StatusOK)
	}))
	defer relay.Close()

	h := &ChatHandler{Client: &http.Client{}}
	client := h.relayClient(relay.URL)

	req, err := http.NewRequest(http.MethodPost, "https://opencode.ai/zen/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("x-relay-target", "https://opencode.ai")
	req.Header.Set("x-relay-path", "/zen/v1/messages")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("relayed request: %v", err)
	}
	defer resp.Body.Close()

	if gotPath != "/zen/v1/messages" {
		t.Errorf("x-relay-path = %q, want the lane the executor chose", gotPath)
	}
}
