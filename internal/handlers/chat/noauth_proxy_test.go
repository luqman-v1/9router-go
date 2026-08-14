package chat

import (
	"testing"

	"9router/proxy/internal/db"
)

func TestGetBestConnection_NoAuth_WithProxyStrategy(t *testing.T) {
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
		Name:     "mimo-pool-1",
		ProxyURL: "http://proxy1.example.com:8080",
		Type:     "http",
	})
	if err != nil {
		t.Fatalf("insert pool: %v", err)
	}
	poolID := pool["id"].(string)

	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS settings (id INTEGER PRIMARY KEY, data TEXT);`); err != nil {
		t.Fatalf("create settings table: %v", err)
	}

	// Save settings with providerStrategies
	settingsJSON := `{
		"providerStrategies": {
			"mimo-free": {
				"proxyPoolId": "` + poolID + `",
				"rotateStrategy": "none"
			}
		}
	}`
	if _, err := database.Exec(`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`, settingsJSON); err != nil {
		t.Fatalf("insert settings: %v", err)
	}

	h := NewChatHandler(repo)

	conn, connData, err := h.GetBestConnection("mimo-free", "", nil, "")
	if err != nil {
		t.Fatalf("GetBestConnection failed: %v", err)
	}

	if conn == nil || connData == nil {
		t.Fatal("expected virtual connection for no-auth provider")
	}

	if connData.ProxyPoolID != poolID {
		t.Errorf("expected ProxyPoolID %s from settings strategy, got %s", poolID, connData.ProxyPoolID)
	}
}
