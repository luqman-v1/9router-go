package db

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/vault"
)

// TestVaultNestedProviderSpecificData verifies that when Vault is enabled,
// saving connection data containing nested providerSpecificData credentials redacts them
// from the raw JSON column at rest, and hydration restores them to plaintext.
func TestVaultNestedProviderSpecificData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.sqlite")
	database, cleanup := setupTestDBAt(t, path)
	defer cleanup()

	v, err := vault.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatalf("build vault: %v", err)
	}

	repo := NewRepo(database)
	repo.SetVault(v)

	connID := "conn-nested-1"
	data := `{"baseUrl":"https://example.invalid/v1","providerSpecificData":{"qwenCloudCookie":"cookie-abc-123","consoleApiKey":"key-xyz-789","otherConfig":"keep-me"}}`
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := repo.RawDB().Exec(
		`INSERT INTO providerConnections
		 (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES (?, 'qwen', 'cookie', 'test', 1, 1, ?, ?, ?)`,
		connID, data, now, now,
	); err != nil {
		t.Fatalf("insert connection: %v", err)
	}

	// Trigger seal on write or migration (with time sleep or unique path if called twice in same second)
	time.Sleep(10 * time.Millisecond)
	if _, err := repo.MigratePlaintextCredentials(context.Background(), path); err != nil {
		t.Fatalf("migrate plaintext credentials: %v", err)
	}

	// Verify raw data at rest no longer contains the sensitive cookies/keys
	raw := rawConnectionData(t, repo, connID)
	if strings.Contains(raw, "cookie-abc-123") {
		t.Errorf("raw data at rest still contains qwenCloudCookie: %s", raw)
	}
	if strings.Contains(raw, "key-xyz-789") {
		t.Errorf("raw data at rest still contains consoleApiKey: %s", raw)
	}
	if !strings.Contains(raw, "keep-me") {
		t.Errorf("raw data at rest lost non-secret providerSpecificData config: %s", raw)
	}

	// Verify hydration restores the nested credentials correctly
	conn, err := repo.GetProviderConnectionByID(connID)
	if err != nil {
		t.Fatalf("get connection by ID: %v", err)
	}
	if !strings.Contains(conn.Data, "cookie-abc-123") {
		t.Errorf("hydrated data missing qwenCloudCookie: %s", conn.Data)
	}
	if !strings.Contains(conn.Data, "key-xyz-789") {
		t.Errorf("hydrated data missing consoleApiKey: %s", conn.Data)
	}
}

// TestVaultNestedMigrationPreservesTopLevelAndSealsNested verifies that a row sealed
// with apiKey only, then having providerSpecificData.usageCookie added as plaintext,
// gets successfully reprocessed and migrated: nested field sealed, top-level apiKey still decrypts correctly,
// candidate listing returns that row, and vault counts do not lie.
func TestVaultNestedMigrationPreservesTopLevelAndSealsNested(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.sqlite")
	database, cleanup := setupTestDBAt(t, path)
	defer cleanup()

	v, err := vault.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatalf("build vault: %v", err)
	}

	repo := NewRepo(database)
	repo.SetVault(v)

	connID := "conn-migration-merge"
	// 1. Initial insert with apiKey only (top-level secret)
	initialData := `{"apiKey":"sk-top-level-original","providerSpecificData":{"otherConfig":"visible"}}`
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := repo.RawDB().Exec(
		`INSERT INTO providerConnections
		 (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES (?, 'openai', 'apikey', 'test', 1, 1, ?, ?, ?)`,
		connID, initialData, now, now,
	); err != nil {
		t.Fatalf("insert connection: %v", err)
	}

	// Seal initial data so top-level apiKey is sealed
	if _, err := repo.MigratePlaintextCredentials(context.Background(), path); err != nil {
		t.Fatalf("initial migration: %v", err)
	}

	// Verify sealed
	sealedCount, plaintextCount, err := repo.VaultCounts(context.Background())
	if err != nil {
		t.Fatalf("vault counts: %v", err)
	}
	if sealedCount != 1 || plaintextCount != 0 {
		t.Errorf("after initial seal: sealed=%d, plaintext=%d, want sealed=1, plaintext=0", sealedCount, plaintextCount)
	}

	// 2. Add plaintext nested usageCookie while top-level apiKey is already sealed
	updatedData := `{"providerSpecificData":{"usageCookie":"cookie-nested-secret","otherConfig":"visible"}}`
	if _, err := repo.RawDB().Exec(
		`UPDATE providerConnections SET data = ? WHERE id = ?`,
		updatedData, connID,
	); err != nil {
		t.Fatalf("update connection with nested plaintext: %v", err)
	}

	// Check that candidate listing returns this row even though top-level ciphertext exists
	candidates, err := repo.ListConnectionsNeedingSeal(context.Background())
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	foundCandidate := false
	for _, c := range candidates {
		if c.ID == connID {
			foundCandidate = true
			break
		}
	}
	if !foundCandidate {
		t.Errorf("ListConnectionsNeedingSeal failed to return row with unsealed nested secret when top-level ciphertext already existed")
	}

	// Check that vault counts report it as plaintext (not fully sealed)
	_, plaintextCount, err = repo.VaultCounts(context.Background())
	if err != nil {
		t.Fatalf("vault counts after adding nested plaintext: %v", err)
	}
	if plaintextCount != 1 {
		t.Errorf("after adding nested plaintext: plaintext=%d, want 1", plaintextCount)
	}

	// 3. Run migration again (pass empty path so backup step is skipped or doesn't conflict on second call in same second)
	if _, err := repo.MigratePlaintextCredentials(context.Background(), ""); err != nil {
		t.Fatalf("second migration: %v", err)
	}

	// 4. Verify nested field is sealed (not present in raw data) AND top-level apiKey still decrypts to original value
	raw := rawConnectionData(t, repo, connID)
	if strings.Contains(raw, "cookie-nested-secret") {
		t.Errorf("raw data at rest still contains unsealed usageCookie: %s", raw)
	}

	conn, err := repo.GetProviderConnectionByID(connID)
	if err != nil {
		t.Fatalf("get connection by ID: %v", err)
	}

	// Verify top-level apiKey decrypted correctly
	fields, ok := decodeObject(conn.Data)
	if !ok {
		t.Fatalf("hydrated data is not a valid JSON object: %s", conn.Data)
	}
	if apiKey, _ := fields["apiKey"].(string); apiKey != "sk-top-level-original" {
		t.Errorf("top-level apiKey lost or corrupted after merge migration: got %q, want %q", apiKey, "sk-top-level-original")
	}

	// Verify nested usageCookie hydrated correctly
	psd, ok := fields["providerSpecificData"].(map[string]any)
	if !ok {
		t.Fatalf("hydrated data missing providerSpecificData")
	}
	if cookie, _ := psd["usageCookie"].(string); cookie != "cookie-nested-secret" {
		t.Errorf("nested usageCookie not restored on hydration: got %q, want %q", cookie, "cookie-nested-secret")
	}

	// Final vault counts check
	sealedCount, plaintextCount, err = repo.VaultCounts(context.Background())
	if err != nil {
		t.Fatalf("final vault counts: %v", err)
	}
	if sealedCount != 1 || plaintextCount != 0 {
		t.Errorf("final vault counts: sealed=%d, plaintext=%d, want sealed=1, plaintext=0", sealedCount, plaintextCount)
	}
}

// TestVaultNestedUnopenableSlot verifies that if an existing SlotSecret ciphertext
// cannot be opened (e.g. wrong master key or corrupted slot), sealing new nested
// secrets does not overwrite or destroy the existing top-level sealed credential.
func TestVaultNestedUnopenableSlot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.sqlite")
	database, cleanup := setupTestDBAt(t, path)
	defer cleanup()

	repo := NewRepo(database)
	repo.SetVault(vault.NewFromEnv())

	const connID = "conn-unopenable"
	initialData := `{"apiKey":"sk-top-level-original"}`
	if _, err := repo.RawDB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, isActive, data, createdAt, updatedAt)
		 VALUES (?, 'openai', 'apikey', 'Test', 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		connID, initialData,
	); err != nil {
		t.Fatalf("insert test connection: %v", err)
	}

	if _, err := repo.MigratePlaintextCredentials(context.Background(), path); err != nil {
		t.Fatalf("initial migration: %v", err)
	}

	// Corrupt secretWrappedDEK so Open fails
	if _, err := repo.RawDB().Exec(
		`UPDATE providerConnections SET secretWrappedDEK = 'invalid-wrapped-dek' WHERE id = ?`,
		connID,
	); err != nil {
		t.Fatalf("corrupt wrapped dek: %v", err)
	}

	updatedData := `{"apiKey":"sk-top-level-original","providerSpecificData":{"usageCookie":"cookie-new-nested"}}`
	if _, err := repo.RawDB().Exec(
		`UPDATE providerConnections SET data = ? WHERE id = ?`,
		updatedData, connID,
	); err != nil {
		t.Fatalf("update connection with nested plaintext: %v", err)
	}

	// Should not error or destroy existing slot when unopenable
	if _, err := repo.MigratePlaintextCredentials(context.Background(), ""); err != nil {
		t.Fatalf("migration with unopenable slot: %v", err)
	}
}
