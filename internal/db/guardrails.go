package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// DefaultGuardrailTenant is the tenant every guardrail row is written under.
// Multi-tenancy is not ported yet, so the column is pinned to one constant
// instead of being left empty: a future tenant column must not have to
// backfill rows written by this version.
const DefaultGuardrailTenant = "default"

// guardrailPolicyColumns is the single column list shared by every read and
// write so a scan and its SELECT can never drift apart.
const guardrailPolicyColumns = `id, tenant_id, scope, scope_id, name, enabled, config, created_at, updated_at`

// GuardrailPolicy is one row of guardrail_policies: the detectors and actions
// that apply at a given scope. Config holds the JSON detector map (see the
// guardrails package for the shape) and is kept opaque here.
type GuardrailPolicy struct {
	ID        string `json:"id"`
	TenantID  string `json:"tenantId"`
	Scope     string `json:"scope"`
	ScopeID   string `json:"scopeId"`
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	Config    string `json:"config"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

// GuardrailLog is one guardrail decision: which detector fired, on which
// request, with which action. Findings holds the JSON array of matches and
// never contains the redacted text itself.
type GuardrailLog struct {
	ID        string `json:"id"`
	TenantID  string `json:"tenantId"`
	RequestID string `json:"requestId,omitempty"`
	APIKeyID  string `json:"apiKeyId,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
	ChainID   string `json:"chainId,omitempty"`
	Detector  string `json:"detector"`
	Direction string `json:"direction"`
	Action    string `json:"action"`
	Severity  string `json:"severity,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Findings  string `json:"findings,omitempty"`
	CreatedAt string `json:"createdAt"`
}

func scanGuardrailPolicy(s interface{ Scan(...any) error }) (*GuardrailPolicy, error) {
	var p GuardrailPolicy
	err := s.Scan(&p.ID, &p.TenantID, &p.Scope, &p.ScopeID, &p.Name, &p.Enabled, &p.Config, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ListGuardrailPolicies returns every enabled policy for a scope, ordered by
// id so the resolver's layering is deterministic. An empty scopeID lists the
// scope-wide (not single-subject) policies.
func (r *Repo) ListGuardrailPolicies(scope, scopeID string) ([]*GuardrailPolicy, error) {
	rows, err := r.db.Query(
		`SELECT `+guardrailPolicyColumns+` FROM guardrail_policies
		 WHERE tenant_id = ? AND scope = ? AND scope_id = ? AND enabled = 1
		 ORDER BY id`,
		DefaultGuardrailTenant, scope, scopeID,
	)
	if err != nil {
		return nil, fmt.Errorf("list guardrail policies %s/%s: %w", scope, scopeID, err)
	}
	defer rows.Close()

	var out []*GuardrailPolicy
	for rows.Next() {
		p, err := scanGuardrailPolicy(rows)
		if err != nil {
			return nil, fmt.Errorf("scan guardrail policy: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list guardrail policies %s/%s: %w", scope, scopeID, err)
	}
	return out, nil
}

// GetGuardrailPolicy fetches one policy by id.
func (r *Repo) GetGuardrailPolicy(id string) (*GuardrailPolicy, error) {
	row := r.db.QueryRow(`SELECT `+guardrailPolicyColumns+` FROM guardrail_policies WHERE id = ?`, id)
	p, err := scanGuardrailPolicy(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get guardrail policy %s: %w", id, err)
	}
	return p, nil
}

// CreateGuardrailPolicy inserts a policy, generating an id and stamping both
// timestamps. The unique (tenant_id, scope, scope_id) index turns a duplicate
// scope into an error the dashboard can surface.
func (r *Repo) CreateGuardrailPolicy(p *GuardrailPolicy) error {
	if p == nil {
		return fmt.Errorf("create guardrail policy: nil policy")
	}
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	if p.Config == "" {
		p.Config = "{}"
	}
	p.TenantID = DefaultGuardrailTenant
	now := time.Now().UTC().Format(time.RFC3339)
	p.CreatedAt = now
	p.UpdatedAt = now

	_, err := r.db.Exec(
		`INSERT INTO guardrail_policies
		 (id, tenant_id, scope, scope_id, name, enabled, config, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.TenantID, p.Scope, p.ScopeID, p.Name, p.Enabled, p.Config, p.CreatedAt, p.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("create guardrail policy %s: %w", p.ID, err)
	}
	return nil
}

// UpdateGuardrailPolicy rewrites the mutable columns of an existing policy.
// created_at is left alone; updated_at always moves.
func (r *Repo) UpdateGuardrailPolicy(p *GuardrailPolicy) error {
	if p == nil || p.ID == "" {
		return fmt.Errorf("update guardrail policy: missing id")
	}
	if p.Config == "" {
		p.Config = "{}"
	}
	p.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	res, err := r.db.Exec(
		`UPDATE guardrail_policies
		 SET scope = ?, scope_id = ?, name = ?, enabled = ?, config = ?, updated_at = ?
		 WHERE id = ? AND tenant_id = ?`,
		p.Scope, p.ScopeID, p.Name, p.Enabled, p.Config, p.UpdatedAt, p.ID, DefaultGuardrailTenant,
	)
	if err != nil {
		return fmt.Errorf("update guardrail policy %s: %w", p.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update guardrail policy %s: %w", p.ID, err)
	}
	if n == 0 {
		return fmt.Errorf("update guardrail policy %s: not found", p.ID)
	}
	return nil
}

// DeleteGuardrailPolicy removes a policy by id.
func (r *Repo) DeleteGuardrailPolicy(id string) error {
	_, err := r.db.Exec(`DELETE FROM guardrail_policies WHERE id = ? AND tenant_id = ?`, id, DefaultGuardrailTenant)
	if err != nil {
		return fmt.Errorf("delete guardrail policy %s: %w", id, err)
	}
	return nil
}

// InsertGuardrailLog writes one decision row. The id and created_at are
// generated here so every caller records the same clock.
func (r *Repo) InsertGuardrailLog(l *GuardrailLog) error {
	if l == nil {
		return fmt.Errorf("insert guardrail log: nil log")
	}
	if l.ID == "" {
		l.ID = uuid.NewString()
	}
	if l.Findings == "" {
		l.Findings = "[]"
	}
	l.TenantID = DefaultGuardrailTenant
	l.CreatedAt = time.Now().UTC().Format(time.RFC3339)

	_, err := r.db.Exec(
		`INSERT INTO guardrail_logs
		 (id, tenant_id, request_id, api_key_id, provider, model, chain_id,
		  detector, direction, action, severity, reason, findings, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.ID, l.TenantID, l.RequestID, l.APIKeyID, l.Provider, l.Model, l.ChainID,
		l.Detector, l.Direction, l.Action, l.Severity, l.Reason, l.Findings, l.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert guardrail log %s: %w", l.Detector, err)
	}
	return nil
}

// ListGuardrailLogs returns the newest decisions first, capped at limit.
func (r *Repo) ListGuardrailLogs(limit int) ([]*GuardrailLog, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := r.db.Query(
		`SELECT id, tenant_id, request_id, api_key_id, provider, model, chain_id,
		        detector, direction, action, severity, reason, findings, created_at
		 FROM guardrail_logs WHERE tenant_id = ?
		 ORDER BY created_at DESC, id DESC LIMIT ?`,
		DefaultGuardrailTenant, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list guardrail logs: %w", err)
	}
	defer rows.Close()

	var out []*GuardrailLog
	for rows.Next() {
		var l GuardrailLog
		if err := rows.Scan(&l.ID, &l.TenantID, &l.RequestID, &l.APIKeyID, &l.Provider, &l.Model,
			&l.ChainID, &l.Detector, &l.Direction, &l.Action, &l.Severity, &l.Reason,
			&l.Findings, &l.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan guardrail log: %w", err)
		}
		out = append(out, &l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list guardrail logs: %w", err)
	}
	return out, nil
}
