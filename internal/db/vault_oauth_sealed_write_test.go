package db

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"9router/proxy/internal/vault"
)

// TestVaultOAuthWriteSealsCredentials pins that credentials written through
// the sealed Repo API never sit plaintext in the data column at rest, and
// that the single-row reader hydrates them back.
//
// OAuth handlers now route connection writes through
// ReplaceProviderConnectionPayload / CreateProviderConnectionFull instead of
// RawDB().Exec, so a vault-enabled install seals access and refresh tokens
// on every write (relogin, token refresh, onboarding). A writer that used
// RawDB().Exec left the tokens in `data` in the clear and, on read-back,
// handed callers ciphertext that json.Unmarshal silently rejects — dropping
// the refresh token and logging the user out on the next rotation.
func TestVaultOAuthWriteSealsCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.sqlite")
	database, cleanup := setupTestDBAt(t, path)
	defer cleanup()

	v, err := vault.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatalf("build vault: %v", err)
	}
	repo := NewRepo(database)
	repo.SetVault(v)

	const connID = "conn-oauth-sealed"
	const refreshToken = "rt-12345-secret"
	const accessToken = "at-12345-secret"

	// New connection via the sealed create path.
	if err := repo.CreateProviderConnectionFull(context.Background(), connID, "gemini-cli", "oauth", "Test", nil,
		`{"apiKey":"`+accessToken+`","refreshToken":"`+refreshToken+`"}`); err != nil {
		t.Fatalf("create connection: %v", err)
	}

	// The credential must not sit in the data column at rest.
	raw := rawConnectionData(t, repo, connID)
	if strings.Contains(raw, accessToken) || strings.Contains(raw, refreshToken) {
		t.Fatalf("raw data at rest still contains credentials: %s", raw)
	}

	// The single-row reader hydrates them back.
	got, err := repo.GetProviderConnectionByID(connID)
	if err != nil {
		t.Fatalf("get connection by ID: %v", err)
	}
	if !strings.Contains(got.Data, accessToken) || !strings.Contains(got.Data, refreshToken) {
		t.Fatalf("single-row reader did not hydrate credentials: %s", got.Data)
	}

	// An overwrite via the sealed update path keeps credentials sealed.
	if err := repo.ReplaceProviderConnectionPayload(context.Background(), connID, "Test",
		`{"apiKey":"`+accessToken+`","refreshToken":"`+refreshToken+`"}`); err != nil {
		t.Fatalf("replace payload: %v", err)
	}
	raw = rawConnectionData(t, repo, connID)
	if strings.Contains(raw, accessToken) || strings.Contains(raw, refreshToken) {
		t.Fatalf("raw data at rest still contains credentials after overwrite: %s", raw)
	}
}
