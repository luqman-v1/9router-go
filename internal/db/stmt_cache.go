package db

import (
	"context"
	"database/sql/driver"
	"sync"
)

// maxCachedStmts bounds the per-connection prepared-statement cache. Query
// text is almost always a code constant, but a few call sites build IN (...)
// lists whose placeholder count varies per request, so the key space is
// technically unbounded; the cap keeps that at a few hundred statements per
// connection instead of a map that grows with traffic (§4.G "bound
// everything that grows"). It is a var so a test can shrink it.
var maxCachedStmts = 256

// stmtCacheConnector wraps the modernc.org/sqlite driver so each connection
// prepares a given statement once and reuses the handle. database/sql does
// not cache statements itself: with a driver that implements QueryerContext
// (modernc does), every db.Query/db.Exec reaches conn.QueryContext, which
// runs sqlite3_prepare_v2 on the query text and finalizes it again after the
// call — a full parse per statement per request. pprof on the request path
// put _sqlite3Parser/_sqlite3VdbeExec at ~15% of samples for exactly this
// reason. The cache removes the parse from every repeat call; SQLite
// re-prepares cached statements itself on schema change (prepare_v2), so
// ALTER TABLE and friends keep working.
type stmtCacheConnector struct {
	inner driver.Driver
	dsn   string
}

func (c *stmtCacheConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.inner.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &stmtCacheConn{inner: conn, stmts: make(map[string]driver.Stmt)}, nil
}

func (c *stmtCacheConnector) Driver() driver.Driver { return c.inner }

// stmtCacheConn is a driver.Conn with a per-connection statement cache.
// database/sql hands one driver.Conn to one goroutine at a time, so the map
// would be safe without the mutex; the lock stays because a *sql.Stmt
// prepared through db.Prepare can be executed from several goroutines and
// each prepare path funnels through here.
type stmtCacheConn struct {
	inner driver.Conn
	mu    sync.Mutex
	stmts map[string]driver.Stmt
}

// cached returns the real prepared statement for query, preparing and
// caching it on first use. At the cap the whole cache is dropped: one
// eviction pass per overflow keeps the bound while leaving hot statements
// cached in the common case.
func (c *stmtCacheConn) cached(ctx context.Context, query string) (driver.Stmt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.stmts[query]; ok {
		return s, nil
	}
	var (
		s   driver.Stmt
		err error
	)
	if pc, ok := c.inner.(driver.ConnPrepareContext); ok {
		s, err = pc.PrepareContext(ctx, query)
	} else {
		s, err = c.inner.Prepare(query)
	}
	if err != nil {
		return nil, err
	}
	if len(c.stmts) >= maxCachedStmts {
		for _, old := range c.stmts {
			_ = old.Close()
		}
		c.stmts = make(map[string]driver.Stmt, maxCachedStmts)
	}
	c.stmts[query] = s
	return s, nil
}

func (c *stmtCacheConn) Prepare(query string) (driver.Stmt, error) {
	s, err := c.cached(context.Background(), query)
	if err != nil {
		return nil, err
	}
	return cachedStmt{inner: s}, nil
}

func (c *stmtCacheConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	s, err := c.cached(ctx, query)
	if err != nil {
		return nil, err
	}
	return cachedStmt{inner: s}, nil
}

// QueryContext and ExecContext intercept the one-shot paths: without these,
// database/sql would call the inner conn's QueryerContext/ExecerContext and
// re-prepare every call, which is the cost this cache exists to remove.
func (c *stmtCacheConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	s, err := c.cached(ctx, query)
	if err != nil {
		return nil, err
	}
	if qc, ok := s.(driver.StmtQueryContext); ok {
		return qc.QueryContext(ctx, args)
	}
	return s.Query(namedToValues(args))
}

func (c *stmtCacheConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	s, err := c.cached(ctx, query)
	if err != nil {
		return nil, err
	}
	if ec, ok := s.(driver.StmtExecContext); ok {
		return ec.ExecContext(ctx, args)
	}
	return s.Exec(namedToValues(args))
}

// namedToValues converts NamedValue args for drivers whose statements do not
// implement the Context interfaces — the same conversion database/sql
// applies on the deprecated path.
func namedToValues(args []driver.NamedValue) []driver.Value {
	vals := make([]driver.Value, len(args))
	for i, a := range args {
		v, err := driver.DefaultParameterConverter.ConvertValue(a.Value)
		if err != nil {
			vals[i] = a.Value
		} else {
			vals[i] = v
		}
	}
	return vals
}

// ResetSession keeps the cache: modernc's ResetSession only checks that the
// connection is still usable — SQLite has no session state to reset, and the
// prepared handles stay valid for the life of the connection.
func (c *stmtCacheConn) ResetSession(ctx context.Context) error {
	if rs, ok := c.inner.(driver.SessionResetter); ok {
		return rs.ResetSession(ctx)
	}
	return nil
}

func (c *stmtCacheConn) IsValid() bool {
	if v, ok := c.inner.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

func (c *stmtCacheConn) Ping(ctx context.Context) error {
	if p, ok := c.inner.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

// Close finalizes every cached statement, then the connection itself.
func (c *stmtCacheConn) Close() error {
	c.mu.Lock()
	stmts := c.stmts
	c.stmts = nil
	c.mu.Unlock()
	for _, s := range stmts {
		_ = s.Close()
	}
	return c.inner.Close()
}

// Begin delegates to the wrapped driver's Begin. staticcheck flags it as
// deprecated, but the database/sql contract requires it when the inner
// driver does not implement ConnBeginTx; the type assertion in BeginTx
// handles that case.
//
//nolint:staticcheck
func (c *stmtCacheConn) Begin() (driver.Tx, error) {
	return c.inner.Begin()
}

func (c *stmtCacheConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if bt, ok := c.inner.(driver.ConnBeginTx); ok {
		return bt.BeginTx(ctx, opts)
	}
	return c.Begin()
}

// cachedStmt is handed to database/sql for the db.Prepare/tx.Prepare paths.
// Its Close is a no-op: database/sql closes the driver statement after every
// one-shot use, and closing the shared handle there would defeat the cache.
// The real statement is finalized by stmtCacheConn.Close.
type cachedStmt struct{ inner driver.Stmt }

func (s cachedStmt) Close() error  { return nil }
func (s cachedStmt) NumInput() int { return s.inner.NumInput() }

// Exec delegates to the wrapped statement's Exec. staticcheck flags it as
// deprecated, but the database/sql contract requires it when the inner
// statement does not implement StmtExecContext; the type assertion in
// ExecContext handles that case.
//
//nolint:staticcheck
func (s cachedStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.inner.Exec(args)
}

// Query delegates to the wrapped statement's Query, for the same reason as
// Exec above.
//
//nolint:staticcheck
func (s cachedStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.inner.Query(args)
}

func (s cachedStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if ec, ok := s.inner.(driver.StmtExecContext); ok {
		return ec.ExecContext(ctx, args)
	}
	// database/sql does not treat ErrSkip as a fallback at the statement
	// level, so convert and call the deprecated method directly.
	return s.inner.Exec(namedToValues(args))
}

func (s cachedStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	if qc, ok := s.inner.(driver.StmtQueryContext); ok {
		return qc.QueryContext(ctx, args)
	}
	return s.inner.Query(namedToValues(args))
}
