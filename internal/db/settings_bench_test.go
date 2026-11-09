package db

import "testing"

// BenchmarkGetSettings measures the settings read the request path pays for
// on every request: a point query plus, when the row changed, a parse. The
// number belongs in the changelog, and it has to be reproducible from the
// repository for a reviewer to check it.
func BenchmarkGetSettings(b *testing.B) {
	database, cleanup := setupTestDB(b)
	defer cleanup()
	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS settings (
		id INTEGER PRIMARY KEY,
		data TEXT NOT NULL
	);`); err != nil {
		b.Fatalf("failed to create settings table: %v", err)
	}
	if _, err := database.Exec(
		`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`,
		`{"providerThinking":{"deepseek":{"mode":"off"}}}`,
	); err != nil {
		b.Fatalf("failed to insert settings: %v", err)
	}
	repo := NewRepo(database)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := repo.GetSettings(); err != nil {
			b.Fatalf("GetSettings failed: %v", err)
		}
	}
}
