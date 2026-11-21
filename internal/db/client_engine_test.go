package db

import (
	"database/sql"
	"testing"
	_ "modernc.org/sqlite"
)

func TestDetectEngine(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
		want Engine
	}{
		{"postgres scheme", "postgres://router:pw@db:5432/router", EnginePostgres},
		{"postgresql scheme", "postgresql://router:pw@db:5432/router", EnginePostgres},
		{"postgres with sslmode query", "postgres://router:pw@db:5432/router?sslmode=disable", EnginePostgres},
		{"sqlite relative path", "data.sqlite", EngineSQLite},
		{"sqlite absolute path", "/root/.9router/db/data.sqlite", EngineSQLite},
		{"sqlite DSN with pragma params", "/root/.9router/db/data.sqlite?_pragma=busy_timeout(5000)", EngineSQLite},
		{"empty defaults to sqlite", "", EngineSQLite},
		{"postgres substring mid-string is not pg", "/data/postgres://backup", EngineSQLite},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectEngine(tt.dsn); got != tt.want {
				t.Fatalf("detectEngine(%q) = %q, want %q", tt.dsn, got, tt.want)
			}
		})
	}
}

// TestEngineOfHandle pins the driver-sniffing path used by callers that open
// a handle outside InitGlobalDatabase (the dbmigrate CLI).
func TestEngineOfHandle(t *testing.T) {
	sqliteDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer sqliteDB.Close()
	if got := engineOfHandle(sqliteDB); got != EngineSQLite {
		t.Errorf("engineOfHandle(sqlite) = %v, want %v", got, EngineSQLite)
	}
	if got := engineOfHandle(nil); got != EngineSQLite {
		t.Errorf("engineOfHandle(nil) = %v, want %v", got, EngineSQLite)
	}
	// pgx path requires a live handle we cannot open offline; the pgx driver
	// concrete type is covered by the deploy/README migration smoke test.
}

// TestPgPoolConnsDefaults pins the PRD §4.3 defaults so an accidental constant
// edit is caught. It also checks the env override path with a parseable value
// and the fallback-to-default path with a garbage value.
func TestPgPoolConnsDefaults(t *testing.T) {
	t.Setenv("DB_MAX_CONNS", "")
	t.Setenv("DB_MAX_IDLE_CONNS", "")
	open, idle := pgPoolConns()
	if open != 50 || idle != 10 {
		t.Fatalf("pgPoolConns() defaults = (%d, %d), want (50, 10)", open, idle)
	}

	t.Setenv("DB_MAX_CONNS", "25")
	t.Setenv("DB_MAX_IDLE_CONNS", "5")
	open, idle = pgPoolConns()
	if open != 25 || idle != 5 {
		t.Fatalf("pgPoolConns() override = (%d, %d), want (25, 5)", open, idle)
	}

	t.Setenv("DB_MAX_CONNS", "not-a-number")
	open, _ = pgPoolConns()
	if open != 50 {
		t.Fatalf("pgPoolConns() garbage env = %d, want default 50", open)
	}
}
