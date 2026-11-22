package config

import "testing"

// TestDatabaseURLSelectsDSN pins that DATABASE_URL wins over DB_PATH so a
// deployment switches engines by setting one variable. Without this the
// PostgreSQL backend shipped in PR #1312 was unreachable through the documented
// deploy/docker-compose.yml environment.
func TestDatabaseURLSelectsDSN(t *testing.T) {
	const dsn = "postgres://router:router@db:5432/router?sslmode=disable"
	tests := []struct {
		name      string
		dsn       string
		dbPath    string
		wantValue string
	}{
		{name: "postgres DSN wins over DB_PATH", dsn: dsn, dbPath: "/tmp/legacy.sqlite", wantValue: dsn},
		{name: "empty DSN falls back to DB_PATH", dsn: "", dbPath: "/tmp/legacy.sqlite", wantValue: "/tmp/legacy.sqlite"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", tt.dsn)
			t.Setenv("DB_PATH", tt.dbPath)
			got := LoadConfig().DatabasePath
			if got != tt.wantValue {
				t.Fatalf("DatabasePath = %q, want %q", got, tt.wantValue)
			}
		})
	}
}
