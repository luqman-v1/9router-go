package db

import (
	"strings"
	"testing"
)

func TestFindApiKeyByLookup(t *testing.T) {
	repo, cleanup := setupDashboardTestDB(t)
	defer cleanup()

	if err := repo.CreateApiKey("k1", "sk-legacy-plaintext", "one", "m1"); err != nil {
		t.Fatalf("CreateApiKey: %v", err)
	}
	if err := repo.HashApiKeyRow("k1", "$argon2id$verifier", "lookup-hash-1", "sk-l…text"); err != nil {
		t.Fatalf("HashApiKeyRow: %v", err)
	}

	got, err := repo.FindApiKeyByLookup("lookup-hash-1")
	if err != nil {
		t.Fatalf("FindApiKeyByLookup: %v", err)
	}
	if got == nil {
		t.Fatal("FindApiKeyByLookup returned nil for a stored lookup hash")
	}
	if got.ID != "k1" {
		t.Fatalf("FindApiKeyByLookup returned row %q, want k1", got.ID)
	}
	if got.KeyHash == nil || *got.KeyHash != "$argon2id$verifier" {
		t.Fatalf("KeyHash = %v, want the stored verifier", got.KeyHash)
	}
	if got.KeyDisplay == nil || *got.KeyDisplay != "sk-l…text" {
		t.Fatalf("KeyDisplay = %v", got.KeyDisplay)
	}

	missing, err := repo.FindApiKeyByLookup("no-such-lookup")
	if err != nil {
		t.Fatalf("FindApiKeyByLookup (miss): %v", err)
	}
	if missing != nil {
		t.Fatalf("FindApiKeyByLookup (miss) returned %+v, want nil", missing)
	}

	empty, err := repo.FindApiKeyByLookup("")
	if err != nil || empty != nil {
		t.Fatalf("FindApiKeyByLookup(\"\") = %+v, %v; want nil, nil", empty, err)
	}
}

// TestHashApiKeyRowBlanksPlaintext is the core storage guarantee: after
// hashing, the plaintext must not be readable from the row.
func TestHashApiKeyRowBlanksPlaintext(t *testing.T) {
	repo, cleanup := setupDashboardTestDB(t)
	defer cleanup()

	const secret = "sk-super-secret-value-1234"
	if err := repo.CreateApiKey("k1", secret, "one", "m1"); err != nil {
		t.Fatalf("CreateApiKey: %v", err)
	}

	// Before hashing, the legacy plaintext path still resolves the key.
	legacy, err := repo.GetApiKeyByKey(secret)
	if err != nil || legacy == nil {
		t.Fatalf("legacy GetApiKeyByKey = %+v, %v; want the row", legacy, err)
	}

	if err := repo.HashApiKeyRow("k1", "verifier", "lookup1", "sk-s…1234"); err != nil {
		t.Fatalf("HashApiKeyRow: %v", err)
	}

	after, err := repo.GetApiKeyByKey(secret)
	if err != nil {
		t.Fatalf("GetApiKeyByKey after hashing: %v", err)
	}
	if after != nil {
		t.Fatal("plaintext key still resolves after the row was hashed")
	}
	byLookup, err := repo.FindApiKeyByLookup("lookup1")
	if err != nil || byLookup == nil {
		t.Fatalf("FindApiKeyByLookup after hashing = %+v, %v", byLookup, err)
	}
	if strings.Contains(byLookup.Key, secret) {
		t.Fatalf("row still carries the plaintext secret: %q", byLookup.Key)
	}
}

// TestHashApiKeyRowSupportsMultipleKeys guards the sentinel: apiKeys.key is
// UNIQUE NOT NULL, so blanking every row to the same value would make the
// second hashed key collide with the first.
func TestHashApiKeyRowSupportsMultipleKeys(t *testing.T) {
	repo, cleanup := setupDashboardTestDB(t)
	defer cleanup()

	for _, id := range []string{"k1", "k2", "k3"} {
		secret := "sk-key-number-" + id
		if err := repo.CreateApiKey(id, secret, "n", "m"); err != nil {
			t.Fatalf("CreateApiKey %s: %v", id, err)
		}
		if err := repo.HashApiKeyRow(id, "verifier-"+id, "lookup-"+id, "sk-k…-"+id); err != nil {
			t.Fatalf("HashApiKeyRow %s: %v", id, err)
		}
		got, err := repo.FindApiKeyByLookup("lookup-" + id)
		if err != nil || got == nil || got.ID != id {
			t.Fatalf("key %s not resolvable by lookup: %+v, %v", id, got, err)
		}
	}
}

func TestHashApiKeyRowMissingID(t *testing.T) {
	repo, cleanup := setupDashboardTestDB(t)
	defer cleanup()

	err := repo.HashApiKeyRow("does-not-exist", "v", "l", "d")
	if err == nil {
		t.Fatal("HashApiKeyRow on a missing id returned no error")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Fatalf("error %q does not name the row", err)
	}
}

// TestValidateApiKeyLegacyPath proves the legacy read path still works for a
// plaintext row, which is what an un-upgraded key looks like.
func TestValidateApiKeyLegacyPath(t *testing.T) {
	repo, cleanup := setupDashboardTestDB(t)
	defer cleanup()

	if err := repo.CreateApiKey("k1", "sk-legacy-token", "n", "m"); err != nil {
		t.Fatalf("CreateApiKey: %v", err)
	}
	ok, err := repo.ValidateApiKey("sk-legacy-token")
	if err != nil || !ok {
		t.Fatalf("ValidateApiKey = %v, %v; want true, nil", ok, err)
	}
	ok, err = repo.ValidateApiKey("sk-nope")
	if err != nil || ok {
		t.Fatalf("ValidateApiKey(unknown) = %v, %v; want false, nil", ok, err)
	}
}
