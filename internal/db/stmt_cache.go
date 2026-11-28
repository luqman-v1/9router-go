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
// prepares a given statement once on the one-shot paths and reuses the
// handle. database/sql does not cache statements itself: with a driver that
// implements QueryerContext (modernc does), every db.Query/db.Exec reaches
// conn.QueryContext, which runs sqlite3_prepare_v2 on the query text and
// finalizes it again after the call — a full parse per statement per
// request. pprof on the request path put _sqlite3Parser/_sqlite3VdbeExec at
// ~15% of samples for exactly this reason. The cache removes the parse from
// every repeat call.
//
// Only the one-shot QueryContext/ExecContext paths are cached. The explicit
// Prepare/PrepareContext path is deliberately NOT cached: a *sql.Stmt handed
// out by db.Prepare keeps its driver handle live until the caller closes it,
// so an eviction pass that finalized cached handles could pull the handle out
// from under a live *sql.Stmt (nil-pointer panic in modernc). database/sql
// already reuses one driver stmt per connection for a *sql.Stmt, so leaving
// Prepare uncached costs nothing relative to upstream.
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
// would be safe without the mutex; the lock stays because a *sql.Stmt can be
// executed from several goroutines and every path funnels through here.
type stmtCacheConn struct {
	inner driver.Conn
	mu    sync.Mutex
	stmts map[string]driver.Stmt
}

// cached returns the prepared statement for query, preparing and caching it
// on first use. Entries are only reachable through the one-shot paths, which
// never retain the handle past the call, so at the cap the whole cache can be
// finalized safely: one eviction pass per overflow keeps the bound while
// leaving hot statements cached in the common case.
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

// Prepare and PrepareContext delegate straight to the inner connection (see
// the stmtCacheConnector doc: the explicit-prepare path is not cached).
func (c *stmtCacheConn) Prepare(query string) (driver.Stmt, error) {
	return c.inner.Prepare(query)
}

func (c *stmtCacheConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if pc, ok := c.inner.(driver.ConnPrepareContext); ok {
		return pc.PrepareContext(ctx, query)
	}
	return c.inner.Prepare(query)
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
	return queryFallback(s, args)
}

func (c *stmtCacheConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	s, err := c.cached(ctx, query)
	if err != nil {
		return nil, err
	}
	if ec, ok := s.(driver.StmtExecContext); ok {
		return ec.ExecContext(ctx, args)
	}
	return execFallback(s, args)
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

// queryFallback and execFallback are the deprecated Stmt.Query/Stmt.Exec
// calls for statements that do not implement the Context interfaces;
// database/sql does not treat ErrSkip as a fallback at the statement level.
//
//nolint:staticcheck
func queryFallback(s driver.Stmt, args []driver.NamedValue) (driver.Rows, error) {
	return s.Query(namedToValues(args))
}

//nolint:staticcheck
func execFallback(s driver.Stmt, args []driver.NamedValue) (driver.Result, error) {
	return s.Exec(namedToValues(args))
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
