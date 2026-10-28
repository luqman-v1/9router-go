package chat

import (
	"testing"

	"9router/proxy/internal/db"
)

// seedAPIKeyRow inserts the apiKeys row the joined plan read needs: that
// query joins apiKeys to plans, so a key that only exists in the model-access
// table is invisible to it.
func seedAPIKeyRow(t testing.TB, repo *db.Repo, id string) {
	t.Helper()
	if _, err := repo.RawDB().Exec(
		`INSERT INTO apiKeys (id, key, name, isActive, createdAt) VALUES (?, ?, ?, 1, ?)`,
		id, "sk-"+id, id, "2026-01-01T00:00:00Z",
	); err != nil {
		t.Fatalf("seed api key %s: %v", id, err)
	}
}
