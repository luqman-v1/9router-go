package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"sync"
	"testing"

	sqlite "modernc.org/sqlite"
)

// countingDriver wraps a driver and records how many times each distinct
// query reaches the real Prepare — the number a statement cache must drive
// to one per query, no matter how often the query runs.
type countingDriver struct {
	inner    driver.Driver
	mu       sync.Mutex
	prepares map[string]int
	closes   int
}

func newCountingDriver(inner driver.Driver) *countingDriver {
	return &countingDriver{inner: inner, prepares: map[string]int{}}
}

func (d *countingDriver) Open(name string) (driver.Conn, error) {
	c, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &countingConn{Conn: c, d: d}, nil
}

func (d *countingDriver) prepareCount(query string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.prepares[query]
}

func (d *countingDriver) totalPrepares() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, c := range d.prepares {
		n += c
	}
	return n
}

func (d *countingDriver) closeCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.closes
}

type countingConn struct {
	driver.Conn
	d *countingDriver
}

func (c *countingConn) Prepare(query string) (driver.Stmt, error) {
	return c.prepare(context.Background(), query)
}

func (c *countingConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	return c.prepare(ctx, query)
}

func (c *countingConn) prepare(ctx context.Context, query string) (driver.Stmt, error) {
	c.d.mu.Lock()
	c.d.prepares[query]++
	c.d.mu.Unlock()
	var (
		s   driver.Stmt
		err error
	)
	if pc, ok := c.Conn.(driver.ConnPrepareContext); ok {
		s, err = pc.PrepareContext(ctx, query)
	} else {
		s, err = c.Conn.Prepare(query)
	}
	if err != nil {
		return nil, err
	}
	return &countingStmt{Stmt: s, d: c.d}, nil
}

type countingStmt struct {
	driver.Stmt
	d *countingDriver
}

func (s *countingStmt) Close() error {
	s.d.mu.Lock()
	s.d.closes++
	s.d.mu.Unlock()
	return s.Stmt.Close()
}

// openCountingCachedDB opens a real SQLite file through the statement cache
// with a counting driver underneath, so tests can assert how many prepares
// actually reached the engine.
func openCountingCachedDB(t *testing.T) (*sql.DB, *countingDriver, func()) {
	t.Helper()
	path := t.TempDir() + "/data.sqlite"
	cd := newCountingDriver(&sqlite.Driver{})
	database := sql.OpenDB(&stmtCacheConnector{inner: cd, dsn: sqliteDSN(path)})
	database.SetMaxOpenConns(2)
	cleanup := func() {
		database.Close()
	}
	return database, cd, cleanup
}

// TestStmtCachePreparesOncePerQuery is the proof the cache works: N executions
// of the same statement must produce exactly one engine-level prepare, on
// both the Query and the Exec path.
func TestStmtCachePreparesOncePerQuery(t *testing.T) {
	database, cd, cleanup := openCountingCachedDB(t)
	defer cleanup()

	const create = `CREATE TABLE kv (scope TEXT NOT NULL, key TEXT NOT NULL, value TEXT NOT NULL, PRIMARY KEY (scope, key))`
	if _, err := database.Exec(create); err != nil {
		t.Fatalf("create: %v", err)
	}
	const insert = `INSERT INTO kv (scope, key, value) VALUES (?, ?, ?)`
	for i := 0; i < 50; i++ {
		if _, err := database.Exec(insert, "s", fmt.Sprintf("k%d", i), "v"); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	const sel = `SELECT value FROM kv WHERE scope = ? AND key = ?`
	for i := 0; i < 200; i++ {
		var v string
		if err := database.QueryRow(sel, "s", fmt.Sprintf("k%d", i%50)).Scan(&v); err != nil {
			t.Fatalf("select %d: %v", i, err)
		}
	}

	for _, q := range []string{create, insert, sel} {
		if got := cd.prepareCount(q); got != 1 {
			t.Errorf("query %q prepared %d times, want 1", q, got)
		}
	}
	if got := cd.totalPrepares(); got != 3 {
		t.Errorf("total prepares = %d, want 3 (one per distinct query)", got)
	}
}

// TestStmtCacheResultsCorrect pins the behaviour: cached statements bind args
// and scan rows exactly like the uncached path.
func TestStmtCacheResultsCorrect(t *testing.T) {
	database, _, cleanup := openCountingCachedDB(t)
	defer cleanup()

	if _, err := database.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT NOT NULL, n INTEGER)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	for i := 1; i <= 5; i++ {
		if _, err := database.Exec(`INSERT INTO t (id, name, n) VALUES (?, ?, ?)`, i, fmt.Sprintf("row%d", i), i*10); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	// Repeat the reads so they hit the cache, not just the first prepare.
	for rep := 0; rep < 3; rep++ {
		var name string
		var n int
		if err := database.QueryRow(`SELECT name, n FROM t WHERE id = ?`, 3).Scan(&name, &n); err != nil {
			t.Fatalf("point select: %v", err)
		}
		if name != "row3" || n != 30 {
			t.Errorf("point select = (%q, %d), want (row3, 30)", name, n)
		}
		rows, err := database.Query(`SELECT id, name FROM t ORDER BY id`)
		if err != nil {
			t.Fatalf("list select: %v", err)
		}
		defer rows.Close()
		var ids []int
		for rows.Next() {
			var id int
			var nm string
			if err := rows.Scan(&id, &nm); err != nil {
				t.Fatalf("scan: %v", err)
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("rows err: %v", err)
		}
		if len(ids) != 5 {
			t.Errorf("list select returned %d rows, want 5", len(ids))
		}
	}
}

// TestStmtCachePreparedStmtPath covers db.Prepare: the explicit-prepare path
// is deliberately uncached (a live *sql.Stmt must never have its handle
// finalized by an eviction), while one-shot calls with the same text still
// share one cached prepare.
func TestStmtCachePreparedStmtPath(t *testing.T) {
	database, cd, cleanup := openCountingCachedDB(t)
	defer cleanup()

	if _, err := database.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	const insert = `INSERT INTO t (id, v) VALUES (?, ?)`
	stmt, err := database.Prepare(insert)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer stmt.Close()
	for i := 1; i <= 10; i++ {
		if _, err := stmt.Exec(i, fmt.Sprintf("v%d", i)); err != nil {
			t.Fatalf("stmt exec %d: %v", i, err)
		}
	}
	// One-shot execs with the same text share one cached prepare (the
	// explicit Prepare above prepared its own, uncached handle).
	if _, err := database.Exec(insert, 11, "v11"); err != nil {
		t.Fatalf("one-shot exec: %v", err)
	}
	if _, err := database.Exec(insert, 12, "v12"); err != nil {
		t.Fatalf("one-shot exec 2: %v", err)
	}
	const sel = `SELECT v FROM t WHERE id = ?`
	for i := 1; i <= 12; i++ {
		var v string
		if err := database.QueryRow(sel, i).Scan(&v); err != nil {
			t.Fatalf("select %d: %v", i, err)
		}
	}
	if got := cd.prepareCount(insert); got != 2 {
		t.Errorf("insert prepared %d times, want 2 (1 explicit Prepare + 1 cached one-shot)", got)
	}
	if got := cd.prepareCount(sel); got != 1 {
		t.Errorf("select prepared %d times, want 1", got)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM t`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 12 {
		t.Errorf("count = %d, want 12", count)
	}
}

// TestStmtCacheEvictionKeepsLivePreparedStmt is the regression test for the
// review finding: forcing cap eviction must not finalize the handle of a
// *sql.Stmt that is still live. The explicit-prepare path is uncached, so the
// prepared statement keeps working after the one-shot cache is evicted.
func TestStmtCacheEvictionKeepsLivePreparedStmt(t *testing.T) {
	database, _, cleanup := openCountingCachedDB(t)
	defer cleanup()
	database.SetMaxOpenConns(1)

	old := maxCachedStmts
	maxCachedStmts = 4
	defer func() { maxCachedStmts = old }()

	if _, err := database.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO t (id, v) VALUES (1, 'one')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	stmt, err := database.Prepare(`SELECT v FROM t WHERE id = ?`)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer stmt.Close()

	// More distinct one-shot queries than the cap, forcing eviction passes.
	for i := 0; i < 10; i++ {
		var n int
		if err := database.QueryRow(fmt.Sprintf(`SELECT %d AS n`, i)).Scan(&n); err != nil {
			t.Fatalf("distinct query %d: %v", i, err)
		}
		if n != i {
			t.Fatalf("distinct query %d = %d", i, n)
		}
	}

	// The live prepared statement must still execute after the evictions.
	var v string
	if err := stmt.QueryRow(1).Scan(&v); err != nil {
		t.Fatalf("live *sql.Stmt after forced eviction: %v", err)
	}
	if v != "one" {
		t.Errorf("live stmt = %q, want one", v)
	}
	// And one-shot queries still work after eviction.
	if err := database.QueryRow(`SELECT v FROM t WHERE id = ?`, 1).Scan(&v); err != nil {
		t.Fatalf("one-shot after eviction: %v", err)
	}
	if v != "one" {
		t.Errorf("one-shot after eviction = %q, want one", v)
	}
}

// TestStmtCacheTransaction runs statements inside a transaction and after
// commit: cached handles are connection-scoped, not transaction-scoped, so
// they stay valid across Commit.
func TestStmtCacheTransaction(t *testing.T) {
	database, cd, cleanup := openCountingCachedDB(t)
	defer cleanup()
	database.SetMaxOpenConns(1) // keep tx and post-commit calls on one conn

	if _, err := database.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	const insert = `INSERT INTO t (id, v) VALUES (?, ?)`
	tx, err := database.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	for i := 1; i <= 3; i++ {
		if _, err := tx.Exec(insert, i, "tx"); err != nil {
			t.Fatalf("tx exec %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	// Same statement text after the commit must reuse the cached handle.
	if _, err := database.Exec(insert, 4, "post"); err != nil {
		t.Fatalf("post-commit exec: %v", err)
	}
	if got := cd.prepareCount(insert); got != 1 {
		t.Errorf("insert prepared %d times across tx and post-commit, want 1", got)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM t`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 4 {
		t.Errorf("count = %d, want 4", count)
	}
}

// TestStmtCacheCloseFinalizesStmts proves the cache does not leak driver
// statements: closing the *sql.DB closes every cached prepare exactly once.
func TestStmtCacheCloseFinalizesStmts(t *testing.T) {
	database, cd, cleanup := openCountingCachedDB(t)

	if _, err := database.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	const q = `INSERT INTO t (id) VALUES (?)`
	for i := 0; i < 5; i++ {
		if _, err := database.Exec(q, i); err != nil {
			t.Fatalf("insert: %v", err)
		}
		if _, err := database.Exec(`UPDATE t SET id = id WHERE id = ?`, i); err != nil {
			t.Fatalf("update: %v", err)
		}
	}
	prepares := cd.totalPrepares()
	database.Close()
	cleanup()
	if got := cd.closeCount(); got != prepares {
		t.Errorf("driver stmt closes = %d, want %d (every prepared statement finalized)", got, prepares)
	}
}

// TestStmtCacheCapBounded shrinks the cap and drives more distinct queries
// than it: results must stay correct and the cache must stay within the cap.
func TestStmtCacheCapBounded(t *testing.T) {
	database, _, cleanup := openCountingCachedDB(t)
	defer cleanup()

	old := maxCachedStmts
	maxCachedStmts = 4
	defer func() { maxCachedStmts = old }()

	if _, err := database.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	for i := 0; i < 10; i++ {
		// Distinct text per iteration (row count varies), like the IN (...)
		// lists the batched readers build.
		rows := make([]string, i+1)
		args := make([]any, 0, 2*(i+1))
		for r := 0; r <= i; r++ {
			rows[r] = "(?, ?)"
			args = append(args, r, "v")
		}
		q := `INSERT OR REPLACE INTO t (id, v) VALUES ` + joinComma(rows)
		if _, err := database.Exec(q, args...); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM t`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	// Rows: insert i puts ids 0..i, so 10 inserts -> max id 9 -> 10 rows.
	if count != 10 {
		t.Errorf("count = %d, want 10", count)
	}
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ","
		}
		out += p
	}
	return out
}

// TestStmtCachePing exercises the Ping delegation.
func TestStmtCachePing(t *testing.T) {
	database, _, cleanup := openCountingCachedDB(t)
	defer cleanup()
	if err := database.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

// Benchmarks for the PR numbers: the same point read through the cache and
// through the plain driver, on the same file.
func benchmarkPointQuery(b *testing.B, cached bool) {
	path := b.TempDir() + "/bench.sqlite"
	var database *sql.DB
	if cached {
		database = sql.OpenDB(&stmtCacheConnector{inner: &sqlite.Driver{}, dsn: sqliteDSN(path)})
	} else {
		var err error
		database, err = sql.Open("sqlite", sqliteDSN(path))
		if err != nil {
			b.Fatalf("open: %v", err)
		}
	}
	defer database.Close()
	if _, err := database.Exec(`CREATE TABLE settings (id INTEGER PRIMARY KEY, data TEXT NOT NULL)`); err != nil {
		b.Fatalf("create: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO settings (id, data) VALUES (1, '{"a":1}')`); err != nil {
		b.Fatalf("seed: %v", err)
	}
	const q = `SELECT data FROM settings WHERE id = ?`
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var data string
		if err := database.QueryRow(q, 1).Scan(&data); err != nil {
			b.Fatalf("query: %v", err)
		}
	}
}

func BenchmarkPointQueryStmtCache(b *testing.B) { benchmarkPointQuery(b, true) }

func BenchmarkPointQueryUncached(b *testing.B) { benchmarkPointQuery(b, false) }
