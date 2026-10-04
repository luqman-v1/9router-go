package db

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

var (
	dbInstance *sql.DB
	dbOnce     sync.Once
	initErr    error
)

// OpenDatabase opens a SQLite database and configures it with WAL mode, normal synchronous mode,
// and other safe concurrency / performance defaults matching the Node/Bun implementation.
func OpenDatabase(path string) (*sql.DB, error) {
	// Ensure the parent directory of the database file exists
	dbDir := filepath.Dir(path)
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return nil, fmt.Errorf("create db dir %s: %w", dbDir, err)
	}
	// The DB stores provider API keys/tokens in plaintext, so keep the file
	// and its directory private to the owning user.
	_ = os.Chmod(dbDir, 0700)

	// Configure per-connection PRAGMAs in the DSN so modernc.org/sqlite applies
	// them to every newly opened connection in the pool (issue #139).
	// busy_timeout is critical in WAL mode to prevent immediate "database is locked" errors during concurrent writes.
	// foreign_keys is required to enforce relational database integrity.
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	dsn := fmt.Sprintf("%s%s_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=temp_store(MEMORY)&_pragma=mmap_size(30000000)&_pragma=cache_size(-64000)&_pragma=foreign_keys(ON)", path, sep)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sql.Open(%s): %w", path, err)
	}

	// Persistent database-level PRAGMA: journal_mode is written to the database file header.
	if _, err = db.Exec("PRAGMA journal_mode = WAL;"); err != nil {
		db.Close()
		return nil, fmt.Errorf("pragma exec journal_mode: %w", err)
	}
	// Restrict the DB file to the owning user (it stores plaintext keys).
	// The file is created by the driver on first open; chmod it now and
	// again on every open to re-assert the permission.
	if err := os.Chmod(path, 0600); err != nil && !errors.Is(err, os.ErrNotExist) {
		db.Close()
		return nil, fmt.Errorf("chmod db file %s: %w", path, err)
	}

	// Configure connection pool limits for SQLite to reduce lock contention
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(time.Hour)

	return db, nil
}

// InitGlobalDatabase initializes the global database connection instance.
func InitGlobalDatabase(path string) error {
	dbOnce.Do(func() {
		dbInstance, initErr = OpenDatabase(path)
	})
	return initErr
}

// GetConnection returns the global database connection.
func GetConnection() (*sql.DB, error) {
	if dbInstance == nil {
		return nil, errors.New("database not initialized, call InitGlobalDatabase first")
	}
	return dbInstance, nil
}
