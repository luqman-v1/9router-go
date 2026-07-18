package db

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
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
		return nil, err
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}

	// Configure PRAGMAs for performance, safety, and concurrency.
	// busy_timeout is critical in WAL mode to prevent immediate "database is locked" errors during concurrent writes.
	// foreign_keys is required to enforce relational database integrity.
	pragmas := `
PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA temp_store = MEMORY;
PRAGMA mmap_size = 30000000;
PRAGMA cache_size = -64000;
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 5000;
`
	if _, err = db.Exec(pragmas); err != nil {
		db.Close()
		return nil, err
	}

	// Configure connection pool limits for SQLite to reduce lock contention
	db.SetMaxOpenConns(50)
	db.SetMaxIdleConns(5)
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
