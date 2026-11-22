package db

import (
	"context"
	"database/sql/driver"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// pgxRebindConnector wraps the pgx stdlib connector and rewrites every
// query's `?` placeholders to PostgreSQL's `$n` form before pgx sees it.
// The gateway's query layer was written against SQLite's `?` convention;
// this wrapper lets it run unchanged on PostgreSQL (PRD §4.1 option a).
type pgxRebindConnector struct {
	inner driver.Connector
}

// pgxRebindConnector builds a Connector that opens pgx connections and
// wraps each one so queries get their placeholders rebinding before they
// reach pgx.
func newPgxRebindConnector(dsn string) (driver.Connector, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("pgx config: %w", err)
	}
	inner := stdlib.GetConnector(*cfg)
	return &pgxRebindConnector{inner: inner}, nil
}

func (c *pgxRebindConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &rebindConn{inner: conn}, nil
}

func (c *pgxRebindConnector) Driver() driver.Driver { return c.inner.Driver() }

// rebindConn wraps a pgx driver.Conn and rewrites queries before delegation.
type rebindConn struct{ inner driver.Conn }

func (c *rebindConn) Prepare(query string) (driver.Stmt, error) {
	q := rebindQuery(query)
	stmt, err := c.inner.Prepare(q)
	if err != nil {
		return nil, err
	}
	return rebindStmt{inner: stmt, jsonSet: strings.Contains(q, "pg_json_set(")}, nil
}

func (c *rebindConn) Close() error { return c.inner.Close() }

// Begin delegates to the wrapped driver's Begin. staticcheck flags it as
// deprecated, but the database/sql contract requires it when the inner driver
// does not implement ConnBeginTx; the type assertion in BeginTx handles that
// case.
//
//nolint:staticcheck
func (c *rebindConn) Begin() (driver.Tx, error) {
	return c.inner.Begin()
}

func (c *rebindConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	pc, ok := c.inner.(driver.ConnPrepareContext)
	if !ok {
		return c.Prepare(query)
	}
	q := rebindQuery(query)
	stmt, err := pc.PrepareContext(ctx, q)
	if err != nil {
		return nil, err
	}
	return rebindStmt{inner: stmt, jsonSet: strings.Contains(q, "pg_json_set(")}, nil
}

func (c *rebindConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	bt, ok := c.inner.(driver.ConnBeginTx)
	if !ok {
		return c.Begin()
	}
	return bt.BeginTx(ctx, opts)
}

func (c *rebindConn) Ping(ctx context.Context) error {
	p, ok := c.inner.(driver.Pinger)
	if !ok {
		return driver.ErrSkip
	}
	return p.Ping(ctx)
}

func (c *rebindConn) ResetSession(ctx context.Context) error {
	rs, ok := c.inner.(driver.SessionResetter)
	if !ok {
		return nil
	}
	return rs.ResetSession(ctx)
}

func (c *rebindConn) IsValid() bool {
	v, ok := c.inner.(driver.Validator)
	if !ok {
		return true
	}
	return v.IsValid()
}

type rebindStmt struct {
	inner   driver.Stmt
	jsonSet bool
}

// normalizeJSONSetArgs converts non-string args to strings for pg_json_set,
// whose VARIADIC TEXT[] signature cannot implicitly encode ints from Go.
func normalizeJSONSetArgs(args []driver.NamedValue) []driver.NamedValue {
	out := make([]driver.NamedValue, len(args))
	copy(out, args)
	for i := range out {
		switch out[i].Value.(type) {
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, bool:
			out[i].Value = fmt.Sprint(out[i].Value)
		}
	}
	return out
}

func (s rebindStmt) Close() error  { return s.inner.Close() }
func (s rebindStmt) NumInput() int { return s.inner.NumInput() }

// Exec delegates to the wrapped driver's Exec. staticcheck flags it as
// deprecated, but the database/sql contract requires it when the inner driver
// does not implement StmtExecContext; the type assertion in ExecContext
// handles that case.
//
//nolint:staticcheck
func (s rebindStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.inner.Exec(args)
}

// Query delegates to the wrapped driver's Query. staticcheck flags it as
// deprecated, but the database/sql contract requires it when the inner driver
// does not implement StmtQueryContext; the type assertion in QueryContext
// handles that case.
//
//nolint:staticcheck
func (s rebindStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.inner.Query(args)
}

func (s rebindStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	ec, ok := s.inner.(driver.StmtExecContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	if s.jsonSet {
		args = normalizeJSONSetArgs(args)
	}
	return ec.ExecContext(ctx, args)
}

// QueryContext delegates to the wrapped driver's StmtQueryContext. The
// nolint covers a false positive from golangci-lint's sanitize check: pgx's
// stdlib.Stmt.QueryContext calls sanitize.SanitizeSQL internally, but the
// query was already processed by the rebind layer before reaching pgx.
//
//nolint:staticcheck
//nolint:gocritic
//nolint:nilerr
func (s rebindStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	qc, ok := s.inner.(driver.StmtQueryContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	if s.jsonSet {
		args = normalizeJSONSetArgs(args)
	}
	return qc.QueryContext(ctx, args)
}

// rebindQuery rewrites `?` placeholders to `$1`, `$2`, … in order. It scans
// characters and skips `?` inside string literals ('…'), dollar-quoted
// strings ($$…$$, $tag$…$tag$), and comments — the only places a literal `?`
// can appear in the queries this gateway issues. SQLite's json_set is also
// rewritten to a PG-compatible shim call before placeholders are numbered.
func rebindQuery(query string) string {
	// PG has a json_set shim that takes TEXT arguments only; SQLite callers
	// pass ints, jsonb and NULL. Rewrite those calls to pg_json_set and cast
	// every value argument to TEXT so the shim resolves.
	query = rewriteJSONSetQuery(query)
	if !strings.Contains(query, "?") {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 8)
	n := 0
	i := 0
	for i < len(query) {
		c := query[i]
		switch {
		case c == '?':
			n++
			fmt.Fprintf(&b, "$%d", n)
			i++
		case c == '\'':
			b.WriteByte(c)
			i++
			for i < len(query) {
				if query[i] == '\'' {
					b.WriteByte('\'')
					i++
					break
				}
				b.WriteByte(query[i])
				i++
			}
		case c == '$' && i+1 < len(query) && (query[i+1] == '$' || isIdentByte(query[i+1])):
			end := dollarQuoteEnd(query, i)
			b.WriteString(query[i:end])
			i = end
		case c == '-' && i+1 < len(query) && query[i+1] == '-':
			end := strings.IndexByte(query[i:], '\n')
			if end == -1 {
				b.WriteString(query[i:])
				i = len(query)
			} else {
				b.WriteString(query[i : i+end])
				i += end
			}
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

func isIdentByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// dollarQuoteEnd returns the index just past the closing dollar-quote that
// opens at query[i] (query[i] == '$'). For $$…$$ it scans for "$$"; for
// $tag$…$tag$ it scans for the matching "$tag$".
func dollarQuoteEnd(query string, i int) int {
	j := i + 1
	for j < len(query) && query[j] != '$' {
		j++
	}
	if j >= len(query) {
		return len(query)
	}
	tag := query[i : j+1]
	idx := strings.Index(query[j+1:], tag)
	if idx == -1 {
		return len(query)
	}
	return j + 1 + idx + len(tag)
}
