package db

import (
	"path/filepath"
	"testing"
)

// TestGetProviderConnectionsOrdersByPriorityThenMostRecentUpdate pins both
// halves of connectionSortOrder: priority wins, and rows that share a priority
// come back most recently updated first.
//
// The second half is not dashboard cosmetics. The chat picker walks this same
// list, so two accounts sitting at the same priority are ordered by updatedAt —
// which nowUTC writes at second resolution. Two same-provider fixtures created
// inside one test therefore tie and fall back to whatever order SQLite returns,
// unless the test states its own priorities. Timestamps here are written
// explicitly rather than by racing the wall clock, because the rule under test
// is the tie-break itself.
func TestGetProviderConnectionsOrdersByPriorityThenMostRecentUpdate(t *testing.T) {
	database, err := OpenDatabase(filepath.Join(t.TempDir(), "connections-order.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer func() {
		if cerr := database.Close(); cerr != nil {
			t.Errorf("close database: %v", cerr)
		}
	}()
	if err := EnsureCoreSchema(database); err != nil {
		t.Fatalf("ensure core schema: %v", err)
	}

	repo := NewRepo(database)
	seed := []struct {
		id       string
		priority int
		updated  string
	}{
		{id: "conn-same-a", priority: 5, updated: "2026-01-02T03:04:05Z"},
		{id: "conn-same-b", priority: 5, updated: "2026-01-02T03:04:06Z"},
		{id: "conn-best", priority: 1, updated: "2026-01-02T03:04:00Z"},
	}
	for _, row := range seed {
		priority := row.priority
		if err := repo.CreateProviderConnectionFull(
			row.id, "tie-order", "apikey", row.id, &priority, `{"apiKey":"sk-tie"}`,
		); err != nil {
			t.Fatalf("seed %s: %v", row.id, err)
		}
		// CreateProviderConnectionFull stamps updatedAt itself; overwrite it so
		// the tie-break is a fact of the fixture, not of the clock.
		if _, err := database.Exec(
			`UPDATE providerConnections SET updatedAt = ? WHERE id = ?`, row.updated, row.id,
		); err != nil {
			t.Fatalf("stamp %s: %v", row.id, err)
		}
	}

	got, err := repo.GetProviderConnections("tie-order", true)
	if err != nil {
		t.Fatalf("GetProviderConnections: %v", err)
	}
	want := []string{"conn-best", "conn-same-b", "conn-same-a"}
	if len(got) != len(want) {
		t.Fatalf("GetProviderConnections returned %d rows, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("row %d = %s, want %s (priority wins, then most recently updated first)", i, got[i].ID, id)
		}
	}
}
