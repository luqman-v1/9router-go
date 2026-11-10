package db

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"9router/proxy/internal/audit"
)

// AuditTenant is the single tenant the audit log uses: 9router-go is a
// single-tenant gateway, and the column exists so the row shape (and the hash
// chain over it) matches the upstream contract a SIEM importer knows.
const AuditTenant = "default"

// AuditRepo is the storage half of the audit trail. It wraps *Repo (which
// owns the handle) and satisfies the audit package's store interfaces, so the
// recorder and Verify depend on the small interfaces rather than the whole
// repository.
type AuditRepo struct {
	db *sql.DB
}

// Audit returns the audit storage bound to this repository.
func (r *Repo) Audit() *AuditRepo { return &AuditRepo{db: r.db} }

// auditColumns is the SELECT list every audit read uses, in Entry field order
// except the chain fields, which are appended last to match the column order.
const auditColumns = `id, tenant_id, actor, actor_type, action, target, resource_type,
	outcome, severity, client_ip, user_agent, http_method, http_route, status_code,
	request_id, trace_id, span_id, detail, chain_id, chain_seq, prev_hash, hash, created_at`

// auditArgs returns the INSERT arguments for one entry, in column order.
func auditArgs(e audit.Entry) []any {
	return []any{
		e.ID, e.TenantID, e.Actor, e.ActorType, e.Action, e.Target, e.ResourceType,
		e.Outcome, e.Severity, e.ClientIP, e.UserAgent, e.HTTPMethod, e.HTTPRoute,
		e.StatusCode, e.RequestID, e.TraceID, e.SpanID, e.Detail, e.ChainID,
		e.ChainSeq, e.PrevHash, e.Hash, e.CreatedAt.UnixMicro(),
	}
}

const auditInsert = `INSERT INTO audit_log (` +
	`id, tenant_id, actor, actor_type, action, target, resource_type, outcome,
	 severity, client_ip, user_agent, http_method, http_route, status_code,
	 request_id, trace_id, span_id, detail, chain_id, chain_seq, prev_hash, hash,
	 created_at)
	 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`

// scanAuditRow reads one SELECT row into e. CreatedAt comes back as a
// microsecond integer and is converted to a time.Time, so Verify can recompute
// the entry hash exactly.
func scanAuditRow(row interface{ Scan(...any) error }, e *audit.Entry) error {
	var createdAtMicro int64
	err := row.Scan(
		&e.ID, &e.TenantID, &e.Actor, &e.ActorType, &e.Action, &e.Target,
		&e.ResourceType, &e.Outcome, &e.Severity, &e.ClientIP, &e.UserAgent,
		&e.HTTPMethod, &e.HTTPRoute, &e.StatusCode, &e.RequestID, &e.TraceID,
		&e.SpanID, &e.Detail, &e.ChainID, &e.ChainSeq, &e.PrevHash, &e.Hash,
		&createdAtMicro,
	)
	if err != nil {
		return err
	}
	e.CreatedAt = time.UnixMicro(createdAtMicro).UTC()
	return nil
}

// BatchInsert writes entries in one transaction, in order. The chain fields
// are already set by the recorder; this is a faithful, ordered write.
func (r *AuditRepo) BatchInsert(ctx context.Context, entries []audit.Entry) error {
	if len(entries) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("audit: begin batch insert: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, auditInsert)
	if err != nil {
		return fmt.Errorf("audit: prepare batch insert: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	for _, e := range entries {
		if _, err := stmt.ExecContext(ctx, auditArgs(e)...); err != nil {
			return fmt.Errorf("audit: batch insert: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("audit: commit batch insert: %w", err)
	}
	return nil
}

// AuditFilter narrows audit queries. Empty fields are ignored.
type AuditFilter struct {
	// Action matches exactly, or as a prefix when it ends in ".*".
	Action       string
	Actor        string
	Outcome      string
	ResourceType string
	Target       string
	// Query is a substring over action, target, actor, route and client IP.
	Query string
	Since time.Time
	Until time.Time
	// BeforeTime/BeforeID continue a newest-first page after the last row of
	// the previous page.
	BeforeTime time.Time
	BeforeID   string
	Limit      int
}

// escapeLike escapes the LIKE wildcards so an operator-supplied query is
// matched literally rather than treated as a pattern.
func escapeLike(s string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(s)
}

// List returns audit entries matching f, newest first, capped at 1000.
func (r *AuditRepo) List(ctx context.Context, f AuditFilter) ([]audit.Entry, error) {
	clauses := []string{"tenant_id = ?"}
	args := []any{AuditTenant}
	if f.Action != "" {
		if prefix, ok := strings.CutSuffix(f.Action, ".*"); ok {
			clauses = append(clauses, `action LIKE ? ESCAPE '\'`)
			args = append(args, escapeLike(prefix)+".%")
		} else {
			clauses = append(clauses, "action = ?")
			args = append(args, f.Action)
		}
	}
	for _, c := range []struct{ col, val string }{
		{"actor", f.Actor}, {"outcome", f.Outcome}, {"resource_type", f.ResourceType}, {"target", f.Target},
	} {
		if c.val != "" {
			clauses = append(clauses, c.col+" = ?")
			args = append(args, c.val)
		}
	}
	if f.Query != "" {
		like := "%" + escapeLike(f.Query) + "%"
		clauses = append(clauses, `(action LIKE ? ESCAPE '\' OR target LIKE ? ESCAPE '\' OR actor LIKE ? ESCAPE '\'
			OR http_route LIKE ? ESCAPE '\' OR client_ip LIKE ? ESCAPE '\')`)
		args = append(args, like, like, like, like, like)
	}
	if !f.Since.IsZero() {
		clauses = append(clauses, "created_at >= ?")
		args = append(args, f.Since.UnixMicro())
	}
	if !f.Until.IsZero() {
		clauses = append(clauses, "created_at < ?")
		args = append(args, f.Until.UnixMicro())
	}
	if !f.BeforeTime.IsZero() {
		ts := f.BeforeTime.UnixMicro()
		clauses = append(clauses, "(created_at < ? OR (created_at = ? AND id < ?))")
		args = append(args, ts, ts, f.BeforeID)
	}
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	q := `SELECT ` + auditColumns + ` FROM audit_log WHERE ` + strings.Join(clauses, " AND ") +
		` ORDER BY created_at DESC, id DESC LIMIT ` + strconv.Itoa(limit)
	return r.query(ctx, q, args...)
}

// Chain returns up to limit rows of one writer chain with chain_seq >
// afterSeq, in chain order. Used to verify the hash chain.
func (r *AuditRepo) Chain(ctx context.Context, chainID string, afterSeq int64, limit int) ([]audit.Entry, error) {
	if limit <= 0 {
		limit = 1000
	}
	q := `SELECT ` + auditColumns + ` FROM audit_log WHERE chain_id = ? AND chain_seq > ?
		ORDER BY chain_seq LIMIT ` + strconv.Itoa(limit)
	return r.query(ctx, q, chainID, afterSeq)
}

// ChainIDs lists every writer chain present in the log.
func (r *AuditRepo) ChainIDs(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT chain_id FROM audit_log WHERE chain_id <> ''`)
	if err != nil {
		return nil, fmt.Errorf("audit: list chains: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("audit: list chains: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// DeleteOlderThan removes entries created before cutoff and reports how many.
func (r *AuditRepo) DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM audit_log WHERE created_at < ?`, cutoff.UnixMicro())
	if err != nil {
		return 0, fmt.Errorf("audit: delete old: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (r *AuditRepo) query(ctx context.Context, q string, args ...any) ([]audit.Entry, error) {
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("audit: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []audit.Entry
	for rows.Next() {
		var e audit.Entry
		if err := scanAuditRow(rows, &e); err != nil {
			return nil, fmt.Errorf("audit: list: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
