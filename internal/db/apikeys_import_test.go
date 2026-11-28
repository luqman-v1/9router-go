package db

import (
	"context"
	"errors"
	"strings"
	"testing"

	"9router/proxy/internal/keikey"
	"9router/proxy/internal/models"
	"github.com/google/uuid"
)

// importTestKey is a valid import secret: `sk-` plus 32 characters of the key
// alphabet, comfortably inside the 32..256 window.
const importTestKey = "sk-importtest-0123456789abcdef01"

// importOtherKey is a second valid secret, used to collide with the first.
const importOtherKey = "sk-importtest-0123456789abcdef99"

func TestImportApiKeyCreatesHashedRow(t *testing.T) {
	r := openEphemeralSchemaDB(t)

	res, err := r.ImportApiKey(context.Background(), ImportApiKeyParams{
		Key:           importTestKey,
		Name:          "Imported",
		AllowedModels: []string{"gpt-4o", " gpt-4o ", ""},
	})
	if err != nil {
		t.Fatalf("ImportApiKey: %v", err)
	}
	if !res.Created {
		t.Fatal("first import reported Created = false")
	}
	if res.Key == nil {
		t.Fatal("first import returned no key row")
	}
	if res.Key.KeyHash == nil || !strings.HasPrefix(*res.Key.KeyHash, "$argon2id$") {
		t.Fatalf("stored keyHash = %v, want an argon2id verifier", res.Key.KeyHash)
	}
	if res.Key.LookupHash == nil || *res.Key.LookupHash != keikey.LookupHash(importTestKey) {
		t.Fatalf("stored lookupHash = %v, want the SHA-256 of the secret", res.Key.LookupHash)
	}
	// The plaintext column holds the sentinel, never the secret: a hashed row
	// must not be readable back through the legacy `WHERE key = ?` path.
	if strings.Contains(res.Key.Key, importTestKey) {
		t.Fatalf("stored key column leaks the plaintext secret: %q", res.Key.Key)
	}
	if !IsHashedKeyRow(res.Key) {
		t.Fatalf("stored key column %q is not the hashed-row sentinel", res.Key.Key)
	}
	if got := res.Key.KeyDisplay; got == nil || strings.Contains(*got, "importtest") {
		t.Fatalf("keyDisplay = %v, want a masked value", got)
	}

	allowed, err := r.GetAllowedModels(res.Key.ID)
	if err != nil {
		t.Fatalf("GetAllowedModels: %v", err)
	}
	if len(allowed) != 1 || allowed[0] != "gpt-4o" {
		t.Fatalf("allowlist = %v, want [gpt-4o] (trimmed and deduped)", allowed)
	}
}

func TestImportApiKeyIsIdempotent(t *testing.T) {
	r := openEphemeralSchemaDB(t)
	params := ImportApiKeyParams{Key: importTestKey, Name: "Imported"}

	first, err := r.ImportApiKey(context.Background(), params)
	if err != nil {
		t.Fatalf("first ImportApiKey: %v", err)
	}
	second, err := r.ImportApiKey(context.Background(), params)
	if err != nil {
		t.Fatalf("second ImportApiKey: %v", err)
	}
	if second.Created {
		t.Fatal("replaying the same import reported Created = true")
	}
	if second.Key == nil || second.Key.ID != first.Key.ID {
		t.Fatalf("replay returned %v, want the original row %s", second.Key, first.Key.ID)
	}
	// The retry must not have rewritten the stored secret.
	if second.Key.KeyHash == nil || *second.Key.KeyHash != *first.Key.KeyHash {
		t.Fatal("replay rewrote the stored argon2id verifier")
	}
	keys, err := r.GetApiKeys()
	if err != nil {
		t.Fatalf("GetApiKeys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("replay left %d rows, want 1", len(keys))
	}
}

func TestImportApiKeyRefusesConflicts(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, r *Repo) ImportApiKeyParams
		body  ImportApiKeyParams
		// wantErr is the sentinel the call must return: a body the importer
		// cannot act on is invalid, a body that would overwrite stored state is
		// a conflict.
		wantErr error
	}{
		{
			name: "id already holds a different key",
			setup: func(t *testing.T, r *Repo) ImportApiKeyParams {
				return ImportApiKeyParams{ID: mustImport(t, r, importTestKey).Key.ID}
			},
			body:    ImportApiKeyParams{Key: importOtherKey},
			wantErr: ErrApiKeyImportConflict,
		},
		{
			// The id is taken and the secret is not the one it holds: the
			// caller is asking to overwrite a row, which import never does.
			name: "id holds a different key while the secret is free",
			setup: func(t *testing.T, r *Repo) ImportApiKeyParams {
				return ImportApiKeyParams{ID: mustImport(t, r, importTestKey).Key.ID}
			},
			body:    ImportApiKeyParams{Key: importOtherKey},
			wantErr: ErrApiKeyImportConflict,
		},
		{
			name: "reserved environment key",
			setup: func(t *testing.T, r *Repo) ImportApiKeyParams {
				t.Setenv(reservedApiKeyEnv, importTestKey)
				return ImportApiKeyParams{}
			},
			body:    ImportApiKeyParams{Key: importTestKey},
			wantErr: ErrApiKeyImportConflict,
		},
		{
			name:    "key too short",
			body:    ImportApiKeyParams{Key: "sk-short"},
			wantErr: ErrApiKeyImportInvalid,
		},
		{
			name:    "key with a trailing newline",
			body:    ImportApiKeyParams{Key: importTestKey + "\n"},
			wantErr: ErrApiKeyImportInvalid,
		},
		{
			name:    "key without the sk- prefix",
			body:    ImportApiKeyParams{Key: strings.TrimPrefix(importTestKey, "sk-")},
			wantErr: ErrApiKeyImportInvalid,
		},
		{
			name:    "id is not a UUID",
			body:    ImportApiKeyParams{ID: "not-a-uuid", Key: importTestKey},
			wantErr: ErrApiKeyImportInvalid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := openEphemeralSchemaDB(t)
			body := tt.body
			if tt.setup != nil {
				seed := tt.setup(t, r)
				if body.ID == "" {
					body.ID = seed.ID
				}
			}
			want := tt.wantErr
			if want == nil {
				want = ErrApiKeyImportConflict
			}
			if _, err := r.ImportApiKey(context.Background(), body); !errors.Is(err, want) {
				t.Fatalf("ImportApiKey error = %v, want %v", err, want)
			}
		})
	}
}

func TestImportApiKeyConflictLeavesNoRowBehind(t *testing.T) {
	r := openEphemeralSchemaDB(t)
	existing := mustImport(t, r, importTestKey)

	_, err := r.ImportApiKey(context.Background(), ImportApiKeyParams{
		ID:  existing.Key.ID,
		Key: importOtherKey,
	})
	if !errors.Is(err, ErrApiKeyImportConflict) {
		t.Fatalf("ImportApiKey error = %v, want ErrApiKeyImportConflict", err)
	}
	if got := mustGetKey(t, r, existing.Key.ID); got == nil {
		t.Fatal("conflict removed the existing row")
	}
	keys, err := r.GetApiKeys()
	if err != nil {
		t.Fatalf("GetApiKeys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("conflict left %d rows, want 1", len(keys))
	}
}

// TestImportApiKeySameSecretNewIDIsIdempotent pins the replay shape a scripted
// import actually produces: the body carries no id, so the second call mints a
// fresh one and the secret is what has to resolve. It returns the original row
// rather than a conflict or a second row.
func TestImportApiKeySameSecretNewIDIsIdempotent(t *testing.T) {
	r := openEphemeralSchemaDB(t)
	first, err := r.ImportApiKey(context.Background(), ImportApiKeyParams{
		Key:  importTestKey,
		Name: "Imported",
	})
	if err != nil {
		t.Fatalf("first ImportApiKey: %v", err)
	}

	second, err := r.ImportApiKey(context.Background(), ImportApiKeyParams{
		ID:   uuid.NewString(),
		Key:  importTestKey,
		Name: "Renamed by the replay",
	})
	if err != nil {
		t.Fatalf("second ImportApiKey: %v", err)
	}
	if second.Created {
		t.Fatal("replay with a fresh id reported Created = true")
	}
	if second.Key.ID != first.Key.ID {
		t.Fatalf("replay returned id %s, want the original %s", second.Key.ID, first.Key.ID)
	}
	// The name the replay carried must not overwrite the stored row.
	if second.Key.Name == nil || *second.Key.Name != "Imported" {
		t.Fatalf("replay overwrote the stored name: %v", second.Key.Name)
	}
	keys, err := r.GetApiKeys()
	if err != nil {
		t.Fatalf("GetApiKeys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("replay left %d rows, want 1", len(keys))
	}
}

func mustImport(t *testing.T, r *Repo, key string) ImportApiKeyResult {
	t.Helper()
	res, err := r.ImportApiKey(context.Background(), ImportApiKeyParams{Key: key})
	if err != nil {
		t.Fatalf("ImportApiKey(%q): %v", key, err)
	}
	return res
}

func mustGetKey(t *testing.T, r *Repo, id string) *models.APIKey {
	t.Helper()
	k, err := r.GetApiKeyByID(id)
	if err != nil {
		t.Fatalf("GetApiKeyByID(%q): %v", id, err)
	}
	return k
}
