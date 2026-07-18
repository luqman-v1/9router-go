package db

import (
	"os"
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

func TestGlobalDatabase(t *testing.T) {
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

	// 4. Repeated initialization should not return error (sync.Once covers it)
	err = InitGlobalDatabase(tmpFile.Name())
	if err != nil {
		t.Errorf("expected no error on repeated InitGlobalDatabase, got %v", err)
	}
}
