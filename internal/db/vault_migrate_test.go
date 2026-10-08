package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/vault"
)

// migrationEnv is a Repo over a throwaway file database with a vault attached.
// It returns the database path too, because the migration snapshots that file
// before its first write.
func migrationEnv(t *testing.T) (*Repo, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data.sqlite")
	database, cleanup := setupTestDBAt(t, path)
	t.Cleanup(cleanup)

	v, err := vault.New([]byte("migration-master-key-32-bytes!!!"))
	if err != nil {
		t.Fatalf("build vault: %v", err)
	}
	repo := NewRepo(database)
	repo.SetVault(v)
	return repo, path
}

// setupTestDBAt is setupTestDB at a path the caller chooses, which the
// migration needs so it can snapshot the exact file it is about to rewrite.
func setupTestDBAt(t *testing.T, path string) (*sql.DB, func()) {
	t.Helper()
	database, err := OpenDatabase(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	cleanup := func() { database.Close() }
	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS providerConnections (
		id TEXT PRIMARY KEY, provider TEXT NOT NULL, authType TEXT NOT NULL,
		name TEXT, email TEXT, priority INTEGER, isActive INTEGER DEFAULT 1,
		data TEXT NOT NULL, lastUsedAt TEXT, consecutiveUseCount INTEGER DEFAULT 0,
		createdAt TEXT NOT NULL, updatedAt TEXT NOT NULL)`); err != nil {
		cleanup()
		t.Fatalf("create providerConnections: %v", err)
	}
	if err := EnsureAdditiveColumns(database); err != nil {
		cleanup()
		t.Fatalf("additive columns: %v", err)
	}
	return database, cleanup
}

// rawConnectionData reads providerConnections.data without hydrating the
// sealed columns, which is what "at rest" means here.
func rawConnectionData(t *testing.T, repo *Repo, id string) string {
	t.Helper()
	var data string
	if err := repo.RawDB().QueryRow(`SELECT data FROM providerConnections WHERE id = ?`, id).Scan(&data); err != nil {
		t.Fatalf("read raw data %s: %v", id, err)
	}
	return data
}

// seedPlaintextConn stores a connection whose data holds a plaintext
// credential, exactly as an install predating the vault does.
//
// The row is written straight to SQL rather than through the connection
// writer, because a live vault seals on write and would leave the migration
// nothing to find — which is precisely the pre-vault state being reproduced.
func seedPlaintextConn(t *testing.T, repo *Repo, id, secret string) {
	t.Helper()
	data := `{"apiKey":"` + secret + `","baseUrl":"https://example.invalid/v1"}`
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := repo.RawDB().Exec(
		`INSERT INTO providerConnections
		 (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES (?, 'openai', 'apikey', ?, 1, 1, ?, ?, ?)`,
		id, id, data, now, now,
	); err != nil {
		t.Fatalf("seed connection %s: %v", id, err)
	}
}

// TestMigratePlaintextCredentialsSealsExistingRows is the gap this closes: a
// vault switched on for an install that already holds credentials must seal
// them, or the operator believes they are encrypted while nothing is.
func TestMigratePlaintextCredentialsSealsExistingRows(t *testing.T) {
	repo, path := migrationEnv(t)
	seedPlaintextConn(t, repo, "conn-a", "sk-secret-a")
	seedPlaintextConn(t, repo, "conn-b", "sk-secret-b")

	result, err := repo.MigratePlaintextCredentials(path)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if result.Sealed != 2 {
		t.Fatalf("sealed = %d, want 2 (skipped: %v)", result.Sealed, result.Skipped)
	}
	for _, id := range []string{"conn-a", "conn-b"} {
		if stored := rawConnectionData(t, repo, id); strings.Contains(stored, "sk-secret-") {
			t.Errorf("%s still holds its plaintext credential at rest: %s", id, stored)
		}
	}
}

// TestMigratePlaintextCredentialsIsIdempotent is the restart case: boot runs
// this pass every time, so a second pass must not re-seal or churn a row that
// is already sealed.
func TestMigratePlaintextCredentialsIsIdempotent(t *testing.T) {
	repo, path := migrationEnv(t)
	seedPlaintextConn(t, repo, "conn-a", "sk-secret-a")

	if _, err := repo.MigratePlaintextCredentials(path); err != nil {
		t.Fatalf("first migration: %v", err)
	}
	wrapped, ciphertext, ok, err := repo.GetConnectionSealed("conn-a", SlotSecret)
	if err != nil || !ok {
		t.Fatalf("first pass did not seal: ok=%v err=%v", ok, err)
	}

	second, err := repo.MigratePlaintextCredentials(path)
	if err != nil {
		t.Fatalf("second migration: %v", err)
	}
	if second.Sealed != 0 {
		t.Errorf("second pass sealed %d rows, want 0", second.Sealed)
	}
	if w2, c2, _, _ := repo.GetConnectionSealed("conn-a", SlotSecret); w2 != wrapped || c2 != ciphertext {
		t.Error("second pass re-sealed the row; ciphertext must be stable across boots")
	}
}

// TestMigratePlaintextCredentialsKeepsCredentialUsable proves the migration did
// not seal the credential away: the connection still authenticates with the
// same secret, and the non-secret config the model resolver reads survives.
func TestMigratePlaintextCredentialsKeepsCredentialUsable(t *testing.T) {
	repo, path := migrationEnv(t)
	seedPlaintextConn(t, repo, "conn-a", "sk-secret-a")

	if _, err := repo.MigratePlaintextCredentials(path); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	conn, err := repo.GetProviderConnectionByID("conn-a")
	if err != nil || conn == nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(conn.Data, "sk-secret-a") {
		t.Errorf("hydrated data lost the credential: %s", conn.Data)
	}
	// Non-secret config has to survive: the resolver walks these rows on every
	// listing and must never need the master key.
	if !strings.Contains(conn.Data, "example.invalid") {
		t.Errorf("migration dropped non-secret config: %s", conn.Data)
	}
}

// TestMigratePlaintextCredentialsLeavesNonSecrets is the backward-compatibility
// guarantee: a connection with nothing to seal is left alone.
func TestMigratePlaintextCredentialsLeavesNonSecrets(t *testing.T) {
	repo, path := migrationEnv(t)
	data := `{"baseUrl":"https://example.invalid/v1","enabledModels":"gpt-4o"}`
	priority := 1
	if err := repo.CreateProviderConnectionFull("conn-plain", "openai", "apikey", "plain", &priority, data); err != nil {
		t.Fatalf("seed: %v", err)
	}

	result, err := repo.MigratePlaintextCredentials(path)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if result.Sealed != 0 {
		t.Errorf("sealed = %d for a connection with no credential", result.Sealed)
	}
	if stored := rawConnectionData(t, repo, "conn-plain"); !strings.Contains(stored, "example.invalid") {
		t.Errorf("untouched connection was altered: %s", stored)
	}
}

// TestMigratePlaintextCredentialsNoOpWithoutVault is what keeps the feature
// free: an install that never set ROUTER_MASTER_KEY must not be rewritten.
func TestMigratePlaintextCredentialsNoOpWithoutVault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.sqlite")
	database, cleanup := setupTestDBAt(t, path)
	defer cleanup()

	repo := NewRepo(database)
	seedPlaintextConn(t, repo, "conn-a", "sk-secret-a")

	result, err := repo.MigratePlaintextCredentials(path)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if result.Sealed != 0 {
		t.Errorf("sealed %d rows with no vault configured", result.Sealed)
	}
	if stored := rawConnectionData(t, repo, "conn-a"); !strings.Contains(stored, "sk-secret-a") {
		t.Errorf("row was altered without a vault: %s", stored)
	}
}

// TestMigratePlaintextCredentialsTakesBackup is the operator's restore point:
// sealing rewrites every credential row, so a copy has to exist before the
// first write, not after.
func TestMigratePlaintextCredentialsTakesBackup(t *testing.T) {
	repo, path := migrationEnv(t)
	seedPlaintextConn(t, repo, "conn-a", "sk-secret-a")

	if _, err := repo.MigratePlaintextCredentials(path); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	backups, err := filepath.Glob(filepath.Join(filepath.Dir(path), "pre-vault-*.sqlite"))
	if err != nil {
		t.Fatalf("glob backups: %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("found %d pre-migration backups, want 1", len(backups))
	}
	// The backup is a snapshot of the pre-migration state, so it must still
	// hold the plaintext the sealed row no longer has.
	data, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if !strings.Contains(string(data), "sk-secret-a") {
		t.Error("backup was taken after sealing; it cannot restore the original credential")
	}
}
