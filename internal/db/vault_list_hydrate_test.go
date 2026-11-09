package db

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/vault"
)

// TestVaultListHydratesTheCredential pins the list query's hydration.
//
// The single-row readers open the sealed columns before they hand a row back,
// so a caller reading a connection by id sees its credential. The list query
// did not, so every caller that read the credential from a listed row — the
// chat path's connection selection, the dashboard's credential check, the
// Gemini Web rotation loop — saw an empty credential while the row itself was
// perfectly usable. A sealed install would have dispatched every request
// without its key.
func TestVaultListHydratesTheCredential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.sqlite")
	database, cleanup := setupTestDBAt(t, path)
	defer cleanup()

	v, err := vault.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatalf("build vault: %v", err)
	}
	repo := NewRepo(database)
	repo.SetVault(v)

	const connID = "conn-list-hydrate"
	const credential = "sk-list-hydrate-value"
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := repo.RawDB().Exec(
		`INSERT INTO providerConnections
		 (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES (?, 'gemini-web', 'cookie', 'test', 1, 1, ?, ?, ?)`,
		connID, `{"apiKey":"`+credential+`"}`, now, now,
	); err != nil {
		t.Fatalf("insert connection: %v", err)
	}

	time.Sleep(10 * time.Millisecond)
	if _, err := repo.MigratePlaintextCredentials(context.Background(), path); err != nil {
		t.Fatalf("migrate plaintext credentials: %v", err)
	}

	// The credential is not in the data column at rest any more.
	raw := rawConnectionData(t, repo, connID)
	if strings.Contains(raw, credential) {
		t.Fatalf("raw data at rest still contains the credential: %s", raw)
	}

	// The single-row reader hydrates.
	single, err := repo.GetProviderConnectionByID(connID)
	if err != nil {
		t.Fatalf("get connection by ID: %v", err)
	}
	if !strings.Contains(single.Data, credential) {
		t.Fatalf("the single-row reader did not hydrate the credential: %s", single.Data)
	}

	// And so must the list reader.
	listed, err := repo.GetProviderConnections("gemini-web", true)
	if err != nil {
		t.Fatalf("get provider connections: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("listed %d connections, want 1", len(listed))
	}
	if !strings.Contains(listed[0].Data, credential) {
		t.Errorf("the list reader did not hydrate the credential: %s", listed[0].Data)
	}
}
