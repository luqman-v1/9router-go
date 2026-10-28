package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestThoughtSignatureSchemaOnFreshDB proves the persistence table is created by
// the normal schema path, so a fresh install gets it without any extra step.
func TestThoughtSignatureSchemaOnFreshDB(t *testing.T) {
	conn, err := OpenDatabase(filepath.Join(t.TempDir(), "fresh.sqlite"))
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	defer conn.Close()

	if err := EnsureCoreSchema(conn); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}

	var count int
	if err := conn.QueryRow(
		`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = 'thoughtSignatures'`,
	).Scan(&count); err != nil {
		t.Fatalf("inspect schema: %v", err)
	}
	if count != 1 {
		t.Fatalf("thoughtSignatures table missing on fresh schema")
	}
}

func TestThoughtSignatureRepoRoundTrip(t *testing.T) {
	conn, err := OpenDatabase(filepath.Join(t.TempDir(), "roundtrip.sqlite"))
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	defer conn.Close()
	if err := EnsureCoreSchema(conn); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	repo := NewRepo(conn)

	now := time.Now()
	exp := now.Add(time.Hour)
	if err := repo.SaveThoughtSignature(context.Background(), "k1", "sig-1", "gemini", exp); err != nil {
		t.Fatalf("SaveThoughtSignature: %v", err)
	}
	// Upsert, not duplicate.
	if err := repo.SaveThoughtSignature(context.Background(), "k1", "sig-2", "gemini", exp); err != nil {
		t.Fatalf("SaveThoughtSignature upsert: %v", err)
	}
	if err := repo.SaveThoughtSignature(context.Background(), "k_stale", "old", "gemini", now.Add(-time.Minute)); err != nil {
		t.Fatalf("SaveThoughtSignature stale: %v", err)
	}

	rows, err := repo.LoadThoughtSignatures(context.Background(), 10)
	if err != nil {
		t.Fatalf("LoadThoughtSignatures: %v", err)
	}
	// Stored as RFC3339, so compare at second precision.
	if len(rows) != 1 || rows[0].Signature != "sig-2" || rows[0].ExpiresAt.Unix() != exp.Unix() {
		t.Fatalf("unexpected loaded rows: %+v", rows)
	}

	pruned, err := repo.PruneThoughtSignatures(context.Background(), now)
	if err != nil {
		t.Fatalf("PruneThoughtSignatures: %v", err)
	}
	if pruned != 1 {
		t.Fatalf("expected 1 expired row pruned, got %d", pruned)
	}
}
