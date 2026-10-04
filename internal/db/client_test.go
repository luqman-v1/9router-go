package db

import (
	"database/sql"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestOpenDatabase(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test_db_*.sqlite")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	conn, err := OpenDatabase(tmpFile.Name())
	if err != nil {
		t.Fatalf("OpenDatabase failed: %v", err)
	}
	defer conn.Close()

	// Verify journal_mode
	var journalMode string
	err = conn.QueryRow("PRAGMA journal_mode;").Scan(&journalMode)
	if err != nil {
		t.Fatalf("failed to query journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("expected journal_mode to be wal, got %s", journalMode)
	}

	// Verify synchronous
	var synchronous int
	err = conn.QueryRow("PRAGMA synchronous;").Scan(&synchronous)
	if err != nil {
		t.Fatalf("failed to query synchronous: %v", err)
	}
	// NORMAL is represented as 1 (OFF=0, NORMAL=1, FULL=2, EXTRA=3)
	if synchronous != 1 {
		t.Errorf("expected synchronous to be 1 (NORMAL), got %d", synchronous)
	}

	// Verify foreign_keys
	var foreignKeys int
	err = conn.QueryRow("PRAGMA foreign_keys;").Scan(&foreignKeys)
	if err != nil {
		t.Fatalf("failed to query foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Errorf("expected foreign_keys to be enabled (1), got %d", foreignKeys)
	}

	// Verify busy_timeout
	var busyTimeout int
	err = conn.QueryRow("PRAGMA busy_timeout;").Scan(&busyTimeout)
	if err != nil {
		t.Fatalf("failed to query busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Errorf("expected busy_timeout to be 5000, got %d", busyTimeout)
	}
}

func TestPooledConnectionsPragma(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test_db_pool_*.sqlite")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	conn, err := OpenDatabase(tmpFile.Name())
	if err != nil {
		t.Fatalf("OpenDatabase failed: %v", err)
	}
	defer conn.Close()

	ctx := t.Context()
	conns := make([]*sql.Conn, 4)
	for i := range 4 {
		c, err := conn.Conn(ctx)
		if err != nil {
			t.Fatalf("failed to get conn %d: %v", i, err)
		}
		defer c.Close()
		conns[i] = c
	}

	for i, c := range conns {
		var bt int
		if err := c.QueryRowContext(ctx, "PRAGMA busy_timeout;").Scan(&bt); err != nil {
			t.Fatalf("conn %d query busy_timeout failed: %v", i, err)
		}
		if bt != 5000 {
			t.Errorf("conn %d busy_timeout = %d, want 5000", i, bt)
		}

		var syncMode int
		if err := c.QueryRowContext(ctx, "PRAGMA synchronous;").Scan(&syncMode); err != nil {
			t.Fatalf("conn %d query synchronous failed: %v", i, err)
		}
		if syncMode != 1 {
			t.Errorf("conn %d synchronous = %d, want 1 (NORMAL)", i, syncMode)
		}

		var fk int
		if err := c.QueryRowContext(ctx, "PRAGMA foreign_keys;").Scan(&fk); err != nil {
			t.Fatalf("conn %d query foreign_keys failed: %v", i, err)
		}
		if fk != 1 {
			t.Errorf("conn %d foreign_keys = %d, want 1 (ON)", i, fk)
		}

		var ts int
		if err := c.QueryRowContext(ctx, "PRAGMA temp_store;").Scan(&ts); err != nil {
			t.Fatalf("conn %d query temp_store failed: %v", i, err)
		}
		if ts != 2 {
			t.Errorf("conn %d temp_store = %d, want 2 (MEMORY)", i, ts)
		}

		var cs int
		if err := c.QueryRowContext(ctx, "PRAGMA cache_size;").Scan(&cs); err != nil {
			t.Fatalf("conn %d query cache_size failed: %v", i, err)
		}
		if cs != -64000 {
			t.Errorf("conn %d cache_size = %d, want -64000", i, cs)
		}

		var mmap int64
		if err := c.QueryRowContext(ctx, "PRAGMA mmap_size;").Scan(&mmap); err != nil {
			t.Fatalf("conn %d query mmap_size failed: %v", i, err)
		}
		if mmap != 30000000 {
			t.Errorf("conn %d mmap_size = %d, want 30000000", i, mmap)
		}

		var jm string
		if err := c.QueryRowContext(ctx, "PRAGMA journal_mode;").Scan(&jm); err != nil {
			t.Fatalf("conn %d query journal_mode failed: %v", i, err)
		}
		if jm != "wal" {
			t.Errorf("conn %d journal_mode = %s, want wal", i, jm)
		}
	}

	// Also verify connection pool limits
	stats := conn.Stats()
	if stats.MaxOpenConnections != 4 {
		t.Errorf("MaxOpenConnections = %d, want 4", stats.MaxOpenConnections)
	}
}

// The DSN is what actually applies the PRAGMAs to every pooled connection, so
// its shape is a contract: a missing pragma silently reverts that connection to
// SQLite defaults, and a wrong separator corrupts the whole DSN.
func TestSQLiteDSN(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		notIn []string
	}{
		{
			name: "plain path gets a question mark separator",
			path: "/tmp/data.sqlite",
			// journal_mode must stay a post-open Exec: it is a file-header
			// property, not connection state.
			notIn: []string{"journal_mode"},
		},
		{
			name:  "existing query string is extended with an ampersand",
			path:  "file:/tmp/data.sqlite?mode=rw",
			notIn: []string{"?_pragma"},
		},
	}

	// Order matters only for the busy_timeout-first guarantee; the driver sorts
	// that itself, so this pins readability rather than behaviour.
	wantPragmas := []string{
		"busy_timeout(5000)",
		"synchronous(NORMAL)",
		"temp_store(MEMORY)",
		"mmap_size(30000000)",
		"cache_size(-64000)",
		"foreign_keys(ON)",
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sqliteDSN(tt.path)

			if !strings.HasPrefix(got, tt.path+sepFor(tt.path)) {
				t.Errorf("sqliteDSN(%q) = %q, want it to keep the path and append %q", tt.path, got, sepFor(tt.path))
			}

			// The first pragma must lead the query string; everything after the
			// separator is what the driver actually parses.
			q := got[strings.Index(got, sepFor(tt.path)):][1:]
			values, err := url.ParseQuery(q)
			if err != nil {
				t.Fatalf("query %q did not parse: %v", q, err)
			}

			gotPragmas := values["_pragma"]
			if !slices.Equal(gotPragmas, wantPragmas) {
				t.Errorf("_pragma params = %v, want %v", gotPragmas, wantPragmas)
			}

			for _, absent := range tt.notIn {
				if strings.Contains(q, absent) {
					t.Errorf("query %q must not contain %q", q, absent)
				}
			}
		})
	}
}

func sepFor(path string) string {
	if strings.Contains(path, "?") {
		return "&"
	}
	return "?"
}

func TestTimeScanningAsString(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test_db_scan_*.sqlite")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	conn, err := OpenDatabase(tmpFile.Name())
	if err != nil {
		t.Fatalf("OpenDatabase failed: %v", err)
	}
	defer conn.Close()

	_, err = conn.Exec("CREATE TABLE test_dates (id TEXT PRIMARY KEY, created_at TEXT NOT NULL)")
	if err != nil {
		t.Fatalf("failed to create test_dates table: %v", err)
	}

	testTimeStr := "2026-07-18T12:34:56.789Z"
	_, err = conn.Exec("INSERT INTO test_dates (id, created_at) VALUES (?, ?)", "123", testTimeStr)
	if err != nil {
		t.Fatalf("failed to insert test date: %v", err)
	}

	var scannedCreatedAt string
	err = conn.QueryRow("SELECT created_at FROM test_dates WHERE id = ?", "123").Scan(&scannedCreatedAt)
	if err != nil {
		t.Fatalf("failed to scan created_at: %v", err)
	}

	if scannedCreatedAt != testTimeStr {
		t.Errorf("expected scanned created_at to match %s, got %s", testTimeStr, scannedCreatedAt)
	}
}

// TestGlobalDatabase is isolated because it asserts on the process-global
// handle itself: the "not initialized yet" case only holds on a global no
// other test has already claimed, and any test that ran first and opened one
// turns it into a false failure.
func TestGlobalDatabase(t *testing.T) {
	ResetGlobalDatabaseForTest(t)
	// 1. GetConnection before initialization should fail
	_, err := GetConnection()
	if err == nil {
		t.Error("expected error getting connection before initialization, got nil")
	}

	// 2. Initialize global database with in-memory SQLite path
	tmpFile, err := os.CreateTemp("", "test_global_db_*.sqlite")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	err = InitGlobalDatabase(tmpFile.Name())
	if err != nil {
		t.Fatalf("InitGlobalDatabase failed: %v", err)
	}

	// 3. GetConnection should now succeed
	conn, err := GetConnection()
	if err != nil {
		t.Fatalf("GetConnection failed after initialization: %v", err)
	}
	if conn == nil {
		t.Error("expected non-nil database connection")
	}

	// 4. A repeated init is a no-op: the first handle wins and the second
	// path is ignored, so a caller that boots twice never gets two pools on
	// one file.
	other, err := os.CreateTemp("", "test_global_db_other_*.sqlite")
	if err != nil {
		t.Fatalf("failed to create second temp file: %v", err)
	}
	defer os.Remove(other.Name())
	other.Close()

	if err := InitGlobalDatabase(other.Name()); err != nil {
		t.Errorf("expected no error on repeated InitGlobalDatabase, got %v", err)
	}
	same, err := GetConnection()
	if err != nil {
		t.Fatalf("GetConnection after repeated init: %v", err)
	}
	if same != conn {
		t.Error("repeated InitGlobalDatabase replaced the open handle; a second boot must reuse it")
	}
}
