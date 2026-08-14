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

	client := h.GetClientForConnection(connData)
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
