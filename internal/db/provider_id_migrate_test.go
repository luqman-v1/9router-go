package db

import "testing"

func TestMigrateLegacyProviderIDs(t *testing.T) {
	repo := openEphemeralSchemaDB(t)

	if err := repo.CreateProviderConnectionFull(
		"conn-legacy", "ollama-cloud", "apikey", "legacy@example.com", nil, "{}",
	); err != nil {
		t.Fatalf("seed legacy connection: %v", err)
	}
	if err := repo.CreateProviderConnectionFull(
		"conn-canonical", "ollama", "apikey", "canonical@example.com", nil, "{}",
	); err != nil {
		t.Fatalf("seed canonical connection: %v", err)
	}
	if err := repo.CreateProviderConnectionFull(
		"conn-other", "antigravity", "oauth", "other@example.com", nil, "{}",
	); err != nil {
		t.Fatalf("seed unrelated connection: %v", err)
	}

	if err := MigrateLegacyProviderIDs(repo.db); err != nil {
		t.Fatalf("MigrateLegacyProviderIDs failed: %v", err)
	}

	tests := []struct {
		name   string
		connID string
		want   string
	}{
		{"legacy id rewritten to canonical", "conn-legacy", "ollama"},
		{"already-canonical row untouched", "conn-canonical", "ollama"},
		{"unrelated provider untouched", "conn-other", "antigravity"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn, err := repo.GetProviderConnectionByID(tt.connID)
			if err != nil {
				t.Fatalf("GetProviderConnectionByID(%q): %v", tt.connID, err)
			}
			if conn.Provider != tt.want {
				t.Errorf("Provider = %q, want %q", conn.Provider, tt.want)
			}
		})
	}

	// Idempotent: running it again must not error or change anything further.
	if err := MigrateLegacyProviderIDs(repo.db); err != nil {
		t.Fatalf("second MigrateLegacyProviderIDs failed: %v", err)
	}
	conn, err := repo.GetProviderConnectionByID("conn-legacy")
	if err != nil {
		t.Fatalf("GetProviderConnectionByID after second run: %v", err)
	}
	if conn.Provider != "ollama" {
		t.Errorf("Provider after second run = %q, want %q", conn.Provider, "ollama")
	}
}

func TestMigrateLegacyProviderIDsNilDB(t *testing.T) {
	if err := MigrateLegacyProviderIDs(nil); err == nil {
		t.Error("MigrateLegacyProviderIDs(nil) = nil error, want error")
	}
}
