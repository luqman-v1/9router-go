//go:build integration

package dbmigrate

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	coredb "9router/proxy/internal/db"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// TestImportPreservesSeededValues pins the #1312 data-loss bug: EnsureCoreSchema
// seeds settings(id=1) with '{}' on a fresh PG; the import must then replace
// that placeholder with the exported value rather than skip it via ON CONFLICT
// DO NOTHING. CI's pg-smoke only asserted SELECT COUNT(1), which stayed 1 while
// the real value was silently discarded.
func TestImportPreservesSeededValues(t *testing.T) {
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("TEST_PG_DSN not set; needs a live PostgreSQL")
	}

	// Build a fresh SQLite fixture with a non-default settings value.
	dir := t.TempDir()
	src := filepath.Join(dir, "src.sqlite")
	sdb, err := sql.Open("sqlite", src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sdb.Exec(`CREATE TABLE settings(id INTEGER PRIMARY KEY CHECK(id=1), data TEXT NOT NULL);
		INSERT INTO settings VALUES(1, '{"theme":"dark"}')`); err != nil {
		t.Fatal(err)
	}
	sdb.Close()
	dump := filepath.Join(dir, "dump")
	if _, err := Export(context.Background(), src, dump); err != nil {
		t.Fatal(err)
	}

	// Fresh target DB: isolate with a unique schema via search_path is not
	// possible for a DSN, so clear the two seeded tables before the run.
	pg, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	if _, err := pg.Exec(`DELETE FROM settings; DELETE FROM _meta`); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(context.Background(), dsn, dump); err != nil {
		t.Fatal(err)
	}

	var got string
	if err := pg.QueryRow(`SELECT data FROM settings WHERE id=1`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != `{"theme":"dark"}` {
		t.Fatalf("imported settings.data = %q, want exported value", got)
	}

	// The seed must still apply to a genuinely empty table after import.
	if _, err := pg.Exec(`DELETE FROM settings`); err != nil {
		t.Fatal(err)
	}
	if err := coredb.EnsureSchemaPostgres(pg); err != nil {
		t.Fatal(err)
	}
	if err := pg.QueryRow(`SELECT data FROM settings WHERE id=1`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "{}" {
		t.Fatalf("seed after import = %q, want {}", got)
	}
}

// TestResetSequencesSkipsTextPrimaryKey pins the audit_log import failure:
// resetSequences ran COALESCE(MAX(id), 0) on a TEXT primary key, which PG
// rejects with SQLSTATE 42804. A table with a non-integer id has no sequence
// to bump and must be skipped.
func TestResetSequencesSkipsTextPrimaryKey(t *testing.T) {
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("TEST_PG_DSN not set; needs a live PostgreSQL")
	}
	pg, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	if _, err := pg.Exec(`DROP TABLE IF EXISTS dbmigrate_text_pk;
		CREATE TABLE dbmigrate_text_pk (id TEXT PRIMARY KEY, data TEXT)`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pg.Exec(`DROP TABLE dbmigrate_text_pk`) }()
	if err := resetSequences(context.Background(), pg, "dbmigrate_text_pk", []string{"id", "data"}); err != nil {
		t.Fatalf("resetSequences on TEXT pk: %v", err)
	}
}
