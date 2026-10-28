package db

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/vault"
)

// The DeepSeek cookie is a credential: the WAF reads it, and it decides
// whether the site's document path answers the router or challenges it. A
// credential that sits in the database in the clear is a credential that
// leaks with a copy of the file.
//
// The same is true of the userToken, which is the API's own credential —
// that one is the row's `apiKey` and was already sealed.
func TestVaultSealsTheDeepSeekCookie(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.sqlite")
	database, cleanup := setupTestDBAt(t, path)
	defer cleanup()

	v, err := vault.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatalf("build vault: %v", err)
	}
	repo := NewRepo(database)
	repo.SetVault(v)

	connID := "conn-deepseek-1"
	data := `{"apiKey":"the-user-token","providerSpecificData":{"deepSeekCookie":"ds_session_id=the-session-value","otherConfig":"keep-me"}}`
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := repo.RawDB().Exec(
		`INSERT INTO providerConnections
		 (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES (?, 'deepseek-web', 'cookie', 'test', 1, 1, ?, ?, ?)`,
		connID, data, now, now,
	); err != nil {
		t.Fatalf("insert connection: %v", err)
	}

	time.Sleep(10 * time.Millisecond)
	if _, err := repo.MigratePlaintextCredentials(context.Background(), path); err != nil {
		t.Fatalf("migrate plaintext credentials: %v", err)
	}

	raw := rawConnectionData(t, repo, connID)
	if strings.Contains(raw, "the-session-value") {
		t.Errorf("raw data at rest still contains the DeepSeek cookie: %s", raw)
	}
	if strings.Contains(raw, "the-user-token") {
		t.Errorf("raw data at rest still contains the userToken: %s", raw)
	}
	if !strings.Contains(raw, "keep-me") {
		t.Errorf("raw data at rest lost the non-secret provider data: %s", raw)
	}

	// The credential has to come back when it is read: a cookie that is
	// sealed and never restored is a connection that cannot work.
	conn, err := repo.GetProviderConnectionByID(connID)
	if err != nil {
		t.Fatalf("get connection by ID: %v", err)
	}
	if !strings.Contains(conn.Data, "the-session-value") {
		t.Errorf("hydrated data is missing the DeepSeek cookie: %s", conn.Data)
	}
	if !strings.Contains(conn.Data, "the-user-token") {
		t.Errorf("hydrated data is missing the userToken: %s", conn.Data)
	}
}
