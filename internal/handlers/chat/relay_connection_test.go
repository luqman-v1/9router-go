package chat

import (
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

func TestGetProviderConfig_EdgeRelayRewriting(t *testing.T) {
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
		Name:     "vercel-relay",
		ProxyURL: "https://my-relay.vercel.app",
		Type:     "vercel",
	})
	if err != nil {
		t.Fatalf("failed to insert proxy pool: %v", err)
	}

	poolID := pool["id"].(string)

	h := &ChatHandler{
		Repo: repo,
	}

	connData := &ConnectionData{
		ProxyPoolID: poolID,
	}

	cfg, err := h.GetProviderConfig("openai", connData)
	if err != nil {
		t.Fatalf("GetProviderConfig failed: %v", err)
	}

	if cfg.BaseURL != "https://my-relay.vercel.app" {
		t.Errorf("expected BaseURL rewritten to https://my-relay.vercel.app, got %s", cfg.BaseURL)
	}
	if cfg.StaticHeaders["x-relay-target"] != "https://api.openai.com" {
		t.Errorf("expected x-relay-target https://api.openai.com, got %s", cfg.StaticHeaders["x-relay-target"])
	}
	if cfg.StaticHeaders["x-relay-path"] != "/v1/chat/completions" {
		t.Errorf("expected x-relay-path /v1/chat/completions, got %s", cfg.StaticHeaders["x-relay-path"])
	}

	// Make sure original KnownProviders["openai"] was not mutated!
	orig := providers.KnownProviders["openai"]
	if orig.BaseURL == "https://my-relay.vercel.app" {
		t.Error("KnownProviders[openai] was mutated!")
	}
}

func TestGetProviderConfig_EdgeRelay_NoProxyBypass(t *testing.T) {
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
		Name:     "cf-relay",
		ProxyURL: "https://my-relay.workers.dev",
		Type:     "cloudflare",
		NoProxy:  "api.openai.com",
	})
	if err != nil {
		t.Fatalf("failed to insert proxy pool: %v", err)
	}

	poolID := pool["id"].(string)

	h := &ChatHandler{
		Repo: repo,
	}

	connData := &ConnectionData{
		ProxyPoolID: poolID,
	}

	cfg, err := h.GetProviderConfig("openai", connData)
	if err != nil {
		t.Fatalf("GetProviderConfig failed: %v", err)
	}

	// Should NOT be rewritten because api.openai.com is in NoProxy
	if cfg.BaseURL != "https://api.openai.com/v1/chat/completions" {
		t.Errorf("expected BaseURL https://api.openai.com/v1/chat/completions (bypassed), got %s", cfg.BaseURL)
	}
	if _, ok := cfg.StaticHeaders["x-relay-target"]; ok {
		t.Error("x-relay-target should not be present when bypassed")
	}
}
