package db

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// The global connection and its open error, guarded by dbMu. OpenDatabase has
// no meaningful failure mode once the path is validated, so a bool would carry
// no information the error does not.
var (
	dbMu       sync.Mutex
	dbInstance *sql.DB
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
	db.SetMaxOpenConns(sqliteMaxOpenConns)
	db.SetMaxIdleConns(sqliteMaxOpenConns)
	db.SetConnMaxLifetime(time.Hour)

	return db, nil
}

// InitGlobalDatabase opens the process-wide database connection once. Later
// calls are a no-op: production boots one gateway per process, and a second
// boot would silently reopen the same SQLite file behind the first one's
// pooled connections. Tests are the one place that legitimately needs a fresh
// handle, which is what ResetGlobalDatabaseForTest is for.
func InitGlobalDatabase(path string) error {
	dbMu.Lock()
	defer dbMu.Unlock()
	if dbInstance != nil {
		return initErr
	}
	dbInstance, initErr = OpenDatabase(path)
	return initErr
}

// ResetGlobalDatabaseForTest closes and forgets the global connection so the
// next InitGlobalDatabase opens a real one. Without it, a test that boots
// app.DatabaseModule and stops its fx app closes the handle for good, and
// every later boot in the same binary inherits the closed handle and an
// already-cancelled shutdown context — which is exactly what makes
// `go test -shuffle` fail on internal/app in an order-dependent way.
//
// Production never calls this: closing the one real connection mid-process is
// precisely the failure mode the process-wide singleton exists to prevent. It
// mirrors shutdown.TestReset, which restores the sibling process-global.
func ResetGlobalDatabaseForTest(t interface{ Cleanup(func()) }) {
	t.Cleanup(func() { ResetGlobalDatabaseForTesting() })
	ResetGlobalDatabaseForTesting()
}

// ResetGlobalDatabaseForTesting is ResetGlobalDatabaseForTest without the
// testing.T dependency, for callers that manage their own teardown.
func ResetGlobalDatabaseForTesting() {
	dbMu.Lock()
	defer dbMu.Unlock()
	if dbInstance != nil {
		_ = dbInstance.Close()
	}
	dbInstance, initErr = nil, nil
}

// GetConnection returns the global database connection.
func GetConnection() (*sql.DB, error) {
	dbMu.Lock()
	defer dbMu.Unlock()
	if dbInstance == nil {
		return nil, errors.New("database not initialized, call InitGlobalDatabase first")
	}
	return dbInstance, nil
}

// sqliteMaxOpenConns is the connection pool size. It is what turns a
// per-connection page cache into a process-wide one: SQLite applies cache_size
// to each connection separately, so the memory ceiling is this times
// sqliteCacheSizeKB.
const sqliteMaxOpenConns = 4

// sqliteCacheSizeKB caps SQLite's page cache per connection, in kibibytes
// (a negative cache_size). The process-wide ceiling is this times
// sqliteMaxOpenConns — 32 MB at 4 connections.
//
// The limit exists because of what this gateway deletes. requestDetails holds
// ~20 KB of payload per row (MaxLoggedMessages x MaxMessageContentLen of
// request text plus MaxResponseContentLen of response text), and the retention
// loop deletes it in chunks. Measured against 40k such rows — half of them
// past the retention window — the prune pass drove the process working set from
// 44 MB to 161 MB, and it never came back down: the freed pages are handed to
// the OS lazily, so a gateway that has pruned once keeps the high-water mark for
// its lifetime. That memory is not the Go heap either (heapAlloc stayed at
// 0.2 MB throughout); it is the page cache SQLite holds in C.
//
// At -2000 the same prune peaks at 17 MB, and the reads it serves do not
// measurably suffer. Against a 400k-row usageHistory ledger, a 24h window fold
// — what the dashboard polls every five seconds — cost 113 ms cold and 35 ms
// warm at -64000, against 83 ms cold and 72 ms warm here: the cold path is
// faster, because a 64 MB cache has to be filled before it is warm at all, and
// the warm path gives up ~37 ms once per poll. Appends are unaffected (6.1 ms
// vs 6.7 ms per 100-row transaction, inside the noise of a write path that is
// dominated by waiting on the provider). The watermark seek the delta cache
// leans on is a covering-index seek and stays at 0.0 ms either way.
//
// 8 MB per connection rather than SQLite's 2 MB default is deliberate headroom
// for the window fold, which is the one read that benefits measurably from a
// warm cache. It is still an eighth of what 64 MB per connection cost.
const sqliteCacheSizeKB = -8000

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
		"cache_size(" + strconv.Itoa(sqliteCacheSizeKB) + ")",
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
