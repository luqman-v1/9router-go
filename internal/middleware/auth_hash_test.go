package middleware

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/dbtest"
	"9router/proxy/internal/keikey"
)

// newHashTestRepo builds a database with the real schema and no seeded keys,
// so each test owns exactly the rows it inserts. The raw handle is returned
// because legacy fixtures must insert plaintext rows directly.
func newHashTestRepo(t *testing.T) (*db.Repo, *sql.DB, func()) {
	t.Helper()
	tmpFile, err := os.CreateTemp("", "test_auth_hash_*.sqlite")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	tmpFile.Close()

	database, err := db.OpenDatabase(tmpFile.Name())
	if err != nil {
		os.Remove(tmpFile.Name())
		t.Fatalf("OpenDatabase: %v", err)
	}
	if err := dbtest.CreateTables(database); err != nil {
		database.Close()
		os.Remove(tmpFile.Name())
		t.Fatalf("CreateTables: %v", err)
	}
	cleanup := func() {
		database.Close()
		os.Remove(tmpFile.Name())
	}
	return db.NewRepo(database), database, cleanup
}

func insertKey(t *testing.T, database *sql.DB, id, key string, active int) {
	t.Helper()
	_, err := database.Exec(
		`INSERT INTO apiKeys (id, key, name, machineId, isActive, createdAt) VALUES (?, ?, 'n', 'm', ?, '2026-07-18T00:00:00Z')`,
		id, key, active,
	)
	if err != nil {
		t.Fatalf("insert api key %s: %v", id, err)
	}
}

// authRequest runs one request through RequireApiKey and reports the status
// plus whatever key object reached the next handler.
func authRequest(t *testing.T, repo *db.Repo, key string) (int, any) {
	t.Helper()
	var captured any
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		captured = GetAuthenticatedApiKey(r)
	})
	handler := RequireApiKey(repo)(next)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Code, captured
}

// storeHashedKey creates a key and immediately converts its row to the F-6
// shape: verifier, lookup hash, display value, blank plaintext.
func storeHashedKey(t *testing.T, repo *db.Repo, id, secret string) {
	t.Helper()
	if err := repo.CreateApiKey(id, secret, "n", "m"); err != nil {
		t.Fatalf("CreateApiKey: %v", err)
	}
	hash, err := keikey.Hash(secret)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if err := repo.HashApiKeyRow(id, hash, keikey.LookupHash(secret), keikey.Mask(secret)); err != nil {
		t.Fatalf("HashApiKeyRow: %v", err)
	}
}

// TestRequireApiKey_HashedKey is the ordinary F-6 path: the row holds only an
// argon2id verifier and a blank `key` column.
func TestRequireApiKey_HashedKey(t *testing.T) {
	repo, _, cleanup := newHashTestRepo(t)
	defer cleanup()

	const secret = "sk-hashed-secret-abcdef123456"
	storeHashedKey(t, repo, "h1", secret)

	code, captured := authRequest(t, repo, secret)
	if code != http.StatusOK {
		t.Fatalf("hashed key rejected with %d", code)
	}
	if captured == nil {
		t.Fatal("authenticated key not injected into context")
	}

	// A wrong secret that resolves to the same lookup hash must still 401:
	// the row is found by SHA-256, but argon2 has the final say.
	code, _ = authRequest(t, repo, "sk-hashed-secret-abcdef123457")
	if code != http.StatusUnauthorized {
		t.Fatalf("wrong plaintext for a hashed row returned %d, want 401", code)
	}
}

// TestRequireApiKey_PlaintextRowIsNotRewritten guards the property issue #199
// depends on. F-6 hashed a plaintext row in place on first use; doing that
// again would destroy the one thing the dashboard now relies on — reading a
// key back — the first time the key was used.
func TestRequireApiKey_PlaintextRowIsNotRewritten(t *testing.T) {
	repo, database, cleanup := newHashTestRepo(t)
	defer cleanup()

	const secret = "sk-legacy-plaintext-key-99"
	insertKey(t, database, "legacy-1", secret, 1)

	code, _ := authRequest(t, repo, secret)
	if code != http.StatusOK {
		t.Fatalf("plaintext key rejected with %d", code)
	}

	// The row must still be readable by its secret after authenticating.
	after, err := repo.GetApiKeyByKey(secret)
	if err != nil || after == nil {
		t.Fatalf("post-auth GetApiKeyByKey = %+v, %v; want the row", after, err)
	}
	if after.KeyHash != nil && *after.KeyHash != "" {
		t.Error("plaintext row was hashed on use; its secret is no longer readable")
	}
	if got, err := repo.FindApiKeyByLookup(keikey.LookupHash(secret)); err != nil {
		t.Fatalf("FindApiKeyByLookup: %v", err)
	} else if got != nil {
		t.Error("plaintext row was indexed by lookup hash; it should have been left alone")
	}

	// And it keeps working.
	if code, _ := authRequest(t, repo, secret); code != http.StatusOK {
		t.Fatalf("key rejected on second use with %d", code)
	}
}

func TestRequireApiKey_Rejections(t *testing.T) {
	repo, database, cleanup := newHashTestRepo(t)
	defer cleanup()

	insertKey(t, database, "inactive", "sk-inactive-key", 0)
	insertKey(t, database, "expired", "sk-expired-key-1", 1)
	if _, err := database.Exec(`UPDATE apiKeys SET expiresAt = ? WHERE id = 'expired'`, "2000-01-01T00:00:00Z"); err != nil {
		t.Fatalf("set expiresAt: %v", err)
	}

	cases := []struct {
		name string
		key  string
		want int
	}{
		{"missing key", "", http.StatusUnauthorized},
		{"unknown key", "sk-nope", http.StatusUnauthorized},
		{"inactive", "sk-inactive-key", http.StatusUnauthorized},
		{"expired", "sk-expired-key-1", http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, captured := authRequest(t, repo, tc.key)
			if code != tc.want {
				t.Fatalf("status = %d, want %d", code, tc.want)
			}
			if captured != nil {
				t.Fatal("a rejected key still reached the handler")
			}
		})
	}
}

// TestRequireApiKey_CachePopulatedOnSuccess proves the argon2 cost is paid
// once: a successful authentication primes the 5s verification cache.
func TestRequireApiKey_CachePopulatedOnSuccess(t *testing.T) {
	repo, _, cleanup := newHashTestRepo(t)
	defer cleanup()

	const secret = "sk-cached-secret-value-777"
	storeHashedKey(t, repo, "c1", secret)

	if code, _ := authRequest(t, repo, secret); code != http.StatusOK {
		t.Fatalf("first request rejected with %d", code)
	}
	cached, ok := keikey.DefaultAuthCache().Get(keikey.LookupHash(secret))
	if !ok {
		t.Fatal("successful authentication did not populate the cache")
	}
	if cached == nil {
		t.Fatal("cache holds nil for an authenticated key")
	}
	if code, _ := authRequest(t, repo, secret); code != http.StatusOK {
		t.Fatalf("cached request rejected with %d", code)
	}
}

// TestRequireApiKey_CacheNeverHoldsUnverifiedKeys guards the security property
// of the cache: only a key that passed argon2 may enter it.
func TestRequireApiKey_CacheNeverHoldsUnverifiedKeys(t *testing.T) {
	repo, _, cleanup := newHashTestRepo(t)
	defer cleanup()

	const secret = "sk-guard-secret-12345"
	if err := repo.CreateApiKey("g1", secret, "n", "m"); err != nil {
		t.Fatalf("CreateApiKey: %v", err)
	}
	// The lookup hash matches the presented key but the verifier does not.
	hash, err := keikey.Hash("a-different-secret-entirely")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if err := repo.HashApiKeyRow("g1", hash, keikey.LookupHash(secret), keikey.Mask(secret)); err != nil {
		t.Fatalf("HashApiKeyRow: %v", err)
	}

	if code, _ := authRequest(t, repo, secret); code != http.StatusUnauthorized {
		t.Fatalf("mismatched verifier accepted with %d", code)
	}
	if _, ok := keikey.DefaultAuthCache().Get(keikey.LookupHash(secret)); ok {
		t.Fatal("a rejected key was cached")
	}
}
