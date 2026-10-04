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

	// Per-connection PRAGMAs go in the DSN, not through db.Exec: db.Exec only
	// borrows one connection from the pool, while the driver applies every
	// `_pragma` to each connection it opens (issue #139).
	db, err := sql.Open("sqlite", sqliteDSN(path))
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

// sqliteDSN appends the per-connection PRAGMAs to path as driver query
// parameters so modernc.org/sqlite applies them to every connection it opens.
//
// busy_timeout is critical in WAL mode: without it a pooled connection that
// loses the write race returns SQLITE_BUSY immediately instead of waiting.
// foreign_keys keeps integrity enforced on every connection; the schema
// declares no FOREIGN KEY constraints today, so this only closes the gap where
// 3 of 4 connections silently skipped enforcement.
//
// journal_mode is intentionally absent — it is a persistent file-header
// property, so OpenDatabase sets it separately after opening.
func sqliteDSN(path string) string {
	pragmas := []string{
		"busy_timeout(5000)",
		"synchronous(NORMAL)",
		"temp_store(MEMORY)",
		"mmap_size(30000000)",
		"cache_size(-64000)",
		"foreign_keys(ON)",
	}

	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}

	params := make([]string, 0, len(pragmas))
	for _, p := range pragmas {
		params = append(params, "_pragma="+p)
	}

	return path + sep + strings.Join(params, "&")
}
