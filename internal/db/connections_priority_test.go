package db

import (
	"context"

	"testing"
)

func TestResolveDefaultPriority(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewRepo(database)

	// When no connections exist, returns 1
	if p := repo.ResolveDefaultPriority(context.Background(), "antigravity"); p != 1 {
		t.Errorf("expected 1 for empty provider, got %d", p)
	}

	// Insert connection with priority 1
	p1 := 1
	if err := repo.CreateProviderConnectionFull(context.Background(), "conn-1", "antigravity", "oauth", "AG 1", &p1, `{}`); err != nil {
		t.Fatalf("create conn-1: %v", err)
	}

	// Next priority should be 2
	if p := repo.ResolveDefaultPriority(context.Background(), "antigravity"); p != 2 {
		t.Errorf("expected 2 after first connection, got %d", p)
	}
}
