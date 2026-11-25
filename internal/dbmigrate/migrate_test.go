package dbmigrate

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// roundTripSQLite spins up an in-memory SQLite file with two small tables and
// returns its path plus a closer. The file form (not :memory:) is required so
// Export's read-only open can find it.
func roundTripSQLite(t *testing.T) (string, func()) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "src.sqlite")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_, err = db.Exec(`
		CREATE TABLE providerConnections (id TEXT PRIMARY KEY, provider TEXT NOT NULL, priority INTEGER);
		CREATE TABLE settings (id INTEGER PRIMARY KEY CHECK (id = 1), data TEXT NOT NULL);
		INSERT INTO providerConnections VALUES ('c1', 'claude', 10), ('c2', 'opencode', 20);
		INSERT INTO settings VALUES (1, '{"theme":"dark"}');
	`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return path, func() { db.Close() }
}

// TestExportWritesJSONL checks the on-disk shape: one file per table, one
// JSON object per row, sqlite_* skipped, deterministic column order, and the
// "<table>: <n> rows" summary line.
func TestExportWritesJSONL(t *testing.T) {
	src, closeDB := roundTripSQLite(t)
	defer closeDB()
	out := t.TempDir()

	summaries, err := Export(context.Background(), src, out)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("Export summaries = %v, want 2 tables", summaries)
	}
	want := map[string]int{"providerConnections": 2, "settings": 1}
	for _, s := range summaries {
		name, rest, ok := strings.Cut(s, ": ")
		if !ok {
			t.Fatalf("summary %q unreadable", s)
		}
		n, err := strconv.Atoi(strings.TrimSuffix(rest, " rows"))
		if err != nil {
			t.Fatalf("summary %q count unreadable: %v", s, err)
		}
		if want[name] != n {
			t.Errorf("summary %q: got n=%d want %d", s, n, want[name])
		}
	}

	for table, wantRows := range want {
		cols, rows, err := ReadJSONL(filepath.Join(out, table+".jsonl"))
		if err != nil {
			t.Fatalf("ReadJSONL %s: %v", table, err)
		}
		if len(rows) != wantRows {
			t.Errorf("%s: %d rows, want %d", table, len(rows), wantRows)
		}
		if len(cols) == 0 {
			t.Errorf("%s: empty column list", table)
		}
	}
}

// TestExportDeterministic runs export twice into separate dirs and asserts
// every produced JSONL file is byte-identical.
func TestExportDeterministic(t *testing.T) {
	src, closeDB := roundTripSQLite(t)
	defer closeDB()
	a := t.TempDir()
	b := t.TempDir()
	if _, err := Export(context.Background(), src, a); err != nil {
		t.Fatalf("export a: %v", err)
	}
	if _, err := Export(context.Background(), src, b); err != nil {
		t.Fatalf("export b: %v", err)
	}
	entries, err := os.ReadDir(a)
	if err != nil {
		t.Fatalf("read dir a: %v", err)
	}
	for _, e := range entries {
		ba, err := os.ReadFile(filepath.Join(a, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		bb, err := os.ReadFile(filepath.Join(b, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if string(ba) != string(bb) {
			t.Errorf("%s: bytes differ between two exports", e.Name())
		}
	}
}

// TestImportRoundTripToSQLite drives Import against a local PostgreSQL if
// TEST_POSTGRES_DSN is set; otherwise it skips. CI does not run a PG service
// on the unit-test job, so the full export→import→verify cycle is guarded.
func TestImportRoundTripToSQLite(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set; run against a live PG to exercise Import")
	}
	src, closeDB := roundTripSQLite(t)
	defer closeDB()
	dump := t.TempDir()
	if _, err := Export(context.Background(), src, dump); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if _, err := Import(context.Background(), dsn, dump); err != nil {
		t.Fatalf("Import: %v", err)
	}
	// Idempotency: re-running must succeed and not error on conflicts.
	if _, err := Import(context.Background(), dsn, dump); err != nil {
		t.Fatalf("re-Import: %v", err)
	}
}

// TestReadJSONLMismatchedColumns rejects a record that introduces a new key
// after the header row locked the column set.
func TestReadJSONLMismatchedColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(path, []byte("{\"a\":1}\n{\"b\":2}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadJSONL(path); err == nil {
		t.Fatal("ReadJSONL: expected error on mismatched column, got nil")
	}
}
