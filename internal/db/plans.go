package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The plan reads and writes below are ctx-less on purpose, matching the policy
// layer this package already uses (GetAllowedModels, SetAllowedModels,
// ListGuardrailPolicies): the dashboard CRUD handlers and the dispatch gate call
// them synchronously on an already-cancellable request, and threading a context
// through only these eight calls would make them the lone exception rather than
// the rule. Migrating the whole policy layer to QueryContext/ExecContext is a
// change of its own; until then every site carries //nolint:noctx so the finding
// is a deliberate choice rather than an omission.

// Plan represents a reusable policy template that API keys can inherit from.
type Plan struct {
	ID               string
	TenantID         string
	Name             string
	Description      string
	LimitMicros      int64
	LimitTokens      int64
	RPMLimit         int
	TPMLimit         int
	ConcurrencyLimit int
	Period           string
	AlertPct         int
	HardCutoff       bool
	AllowedModels    []string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

const planTable = "plans"
const planSelectCols = "id, tenant_id, name, description, limit_micros, limit_tokens, rpm_limit, tpm_limit, concurrency_limit, period, alert_pct, hard_cutoff, allowed_models, created_at, updated_at"

// CreatePlan inserts a new plan.
func (r *Repo) CreatePlan(p Plan) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("plans: create: nil repo")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.db.Exec( //nolint:noctx
		`INSERT INTO `+planTable+` (id, tenant_id, name, description, limit_micros, limit_tokens, rpm_limit, tpm_limit, concurrency_limit, period, alert_pct, hard_cutoff, allowed_models, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.TenantID, p.Name, p.Description, p.LimitMicros, p.LimitTokens,
		p.RPMLimit, p.TPMLimit, p.ConcurrencyLimit, p.Period,
		p.AlertPct, boolToInt(p.HardCutoff), strings.Join(p.AllowedModels, ","),
		now, now,
	)
	if err != nil {
		return fmt.Errorf("plans: create: %w", err)
	}
	return nil
}

// Budget period values a plan may declare. The enum lives here because both
// the dashboard's validation and the budget engine (F-2) read the same set.
const (
	PlanPeriodDaily   = "daily"
	PlanPeriodWeekly  = "weekly"
	PlanPeriodMonthly = "monthly"
	PlanPeriodTotal   = "total"
)

// IsValidPlanPeriod reports whether p is one of the four budget windows.
func IsValidPlanPeriod(p string) bool {
	switch p {
	case PlanPeriodDaily, PlanPeriodWeekly, PlanPeriodMonthly, PlanPeriodTotal:
		return true
	}
	return false
}

// ErrNoPlansTable reports that this database has no plans table. A database
// created before the plans feature answers this, and a key in such a database
// cannot be on a plan.
//
// It is a distinct error on purpose. A failed plan read is a policy failure
// and fails closed; a database without the table is a database where no key
// is restricted by a plan, which is not an outage. A query that merely fails
// (a closed database, a locked file) is neither, and must not be reported as
// a missing table.
var ErrNoPlansTable = errors.New("plans: table does not exist")

// plansTableMissing reports whether the plans table is absent. A failure to
// answer is an error, not an absence: a broken database must not read as a
// database without plans.
func (r *Repo) plansTableMissing() (bool, error) {
	if r == nil || r.db == nil {
		return true, nil
	}
	var name string
	err := r.db.QueryRow( //nolint:noctx
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ? LIMIT 1`,
		planTable,
	).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return name != planTable, nil
}

// classifyPlanReadError turns a failed plan read into either ErrNoPlansTable
// (the table is absent, so no key here can be on a plan) or the original
// error (a real storage failure).
//
// The table check runs only after a query has already failed, so the happy
// path pays nothing for it.
func (r *Repo) classifyPlanReadError(err error) error {
	if err == nil || errors.Is(err, sql.ErrNoRows) {
		return err
	}
	missing, checkErr := r.plansTableMissing()
	if checkErr == nil && missing {
		return ErrNoPlansTable
	}
	return err
}

// GetPlan returns a single plan by id.
func (r *Repo) GetPlan(id string) (Plan, error) {
	if r == nil || r.db == nil || id == "" {
		return Plan{}, fmt.Errorf("plans: get: nil repo or empty id")
	}
	row := r.db.QueryRow( //nolint:noctx
		`SELECT `+planSelectCols+` FROM `+planTable+` WHERE id = ?`, id,
	)
	plan, err := scanPlanRow(row)
	if err != nil {
		return Plan{}, r.classifyPlanReadError(err)
	}
	return plan, nil
}

// ListPlans returns all plans for a tenant, newest first.
func (r *Repo) ListPlans(tenantID string) ([]Plan, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("plans: list: nil repo")
	}
	rows, err := r.db.Query( //nolint:noctx
		`SELECT `+planSelectCols+` FROM `+planTable+` WHERE tenant_id = ? ORDER BY created_at DESC`,
		tenantID,
	)
	if err != nil {
		return nil, fmt.Errorf("plans: list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Plan
	for rows.Next() {
		p, err := scanPlanRows(rows)
		if err != nil {
			return nil, fmt.Errorf("plans: list scan: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetApiKeyPlan assigns a plan to one key. An empty planID clears the
// assignment, which returns the key to its own columns.
func (r *Repo) SetApiKeyPlan(keyID, planID string) error {
	if r == nil || r.db == nil || keyID == "" {
		return fmt.Errorf("plans: set for key: nil repo or empty key id")
	}
	_, err := r.db.Exec(`UPDATE apiKeys SET planId = ? WHERE id = ?`, planID, keyID) //nolint:noctx
	if err != nil {
		return fmt.Errorf("plans: set for key %s: %w", keyID, err)
	}
	return nil
}

// UpdatePlan modifies an existing plan's mutable fields.
func (r *Repo) UpdatePlan(p Plan) error {
	if r == nil || r.db == nil || p.ID == "" {
		return fmt.Errorf("plans: update: nil repo or empty id")
	}
	res, err := r.db.Exec( //nolint:noctx
		`UPDATE `+planTable+` SET name = ?, description = ?, limit_micros = ?, limit_tokens = ?, rpm_limit = ?, tpm_limit = ?, concurrency_limit = ?, period = ?, alert_pct = ?, hard_cutoff = ?, allowed_models = ?, updated_at = ? WHERE id = ?`,
		p.Name, p.Description, p.LimitMicros, p.LimitTokens,
		p.RPMLimit, p.TPMLimit, p.ConcurrencyLimit, p.Period,
		p.AlertPct, boolToInt(p.HardCutoff), strings.Join(p.AllowedModels, ","),
		time.Now().UTC().Format(time.RFC3339), p.ID,
	)
	if err != nil {
		return fmt.Errorf("plans: update: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("plans: update: %w", sql.ErrNoRows)
	}
	return nil
}

// DeletePlan removes a plan. The caller should verify no API keys reference it.
func (r *Repo) DeletePlan(id string) error {
	if r == nil || r.db == nil || id == "" {
		return fmt.Errorf("plans: delete: nil repo or empty id")
	}
	_, err := r.db.Exec(`DELETE FROM `+planTable+` WHERE id = ?`, id) //nolint:noctx
	if err != nil {
		return fmt.Errorf("plans: delete: %w", err)
	}
	return nil
}

// CountPlanKeys returns the number of API keys assigned to a plan.
func (r *Repo) CountPlanKeys(planID string) (int, error) {
	if r == nil || r.db == nil || planID == "" {
		return 0, fmt.Errorf("plans: count keys: nil repo or empty id")
	}
	var n int
	if err := r.db.QueryRow( //nolint:noctx
		`SELECT COUNT(*) FROM apiKeys WHERE planId = ?`, planID,
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("plans: count keys: %w", err)
	}
	return n, nil
}

// GetPlansForKeys returns the plan for every key id in one query, so a
// dashboard listing of N keys costs 1 query instead of N. Keys without a
// plan are present with a zero Plan, keeping the caller's "absent ==
// no plan" rule uniform.
func (r *Repo) GetPlansForKeys(ids []string) (map[string]Plan, error) {
	out := make(map[string]Plan, len(ids))
	if r == nil || r.db == nil || len(ids) == 0 {
		return out, nil
	}
	placeholders := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out[id] = Plan{}
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	if len(placeholders) == 0 {
		return out, nil
	}
	query := `SELECT k.id, ` + planSelectColsPrefixed() +
		` FROM apiKeys k JOIN ` + planTable + ` p ON k.planId = p.id WHERE k.id IN (` +
		strings.Join(placeholders, ",") + `)`
	rows, err := r.db.Query(query, args...) //nolint:noctx
	if err != nil {
		return nil, r.classifyPlanReadError(fmt.Errorf("plans: get for keys: %w", err))
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var keyID string
		p, err := scanPlanRowsPrefixed(rows, &keyID)
		if err != nil {
			return nil, fmt.Errorf("plans: get for keys scan: %w", err)
		}
		out[keyID] = p
	}
	return out, rows.Err()
}

// HasPlansTable reports whether the plans table exists. A database created
// before F-3 answers false, which keeps a missing table a "no plan" case
// instead of a startup failure.
func (r *Repo) HasPlansTable() bool {
	if r == nil || r.db == nil {
		return false
	}
	var name string
	err := r.db.QueryRow( //nolint:noctx
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ? LIMIT 1`,
		planTable,
	).Scan(&name)
	if err == sql.ErrNoRows {
		return false
	}
	return err == nil && name == planTable
}

// planSelectColsPrefixed is the plan column list qualified with the p alias
// for the GetPlansForKeys join.
func planSelectColsPrefixed() string {
	return "p.id, p.tenant_id, p.name, p.description, p.limit_micros, p.limit_tokens, p.rpm_limit, p.tpm_limit, p.concurrency_limit, p.period, p.alert_pct, p.hard_cutoff, p.allowed_models, p.created_at, p.updated_at"
}

// rowScanner abstracts sql.Row and *sql.Rows for scanPlanRow.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanPlanRow scans one row into a Plan using the shared column order.
func scanPlanRow(row rowScanner) (Plan, error) {
	var p Plan
	var hardCut int
	var created, updated string
	var allowedModelsStr string
	err := row.Scan(
		&p.ID, &p.TenantID, &p.Name, &p.Description,
		&p.LimitMicros, &p.LimitTokens,
		&p.RPMLimit, &p.TPMLimit, &p.ConcurrencyLimit,
		&p.Period, &p.AlertPct, &hardCut, &allowedModelsStr, &created, &updated,
	)
	if err != nil {
		return Plan{}, err
	}
	p.HardCutoff = hardCut != 0
	p.CreatedAt = parsePlanTime(created)
	p.UpdatedAt = parsePlanTime(updated)
	p.AllowedModels = splitPlanModels(allowedModelsStr)
	return p, nil
}

// scanPlanRows scans one *sql.Rows row into a Plan.
func scanPlanRows(rows *sql.Rows) (Plan, error) {
	return scanPlanRow(rows)
}

// scanPlanRowsPrefixed scans a GetPlansForKeys join row: the key id first,
// then the plan columns in the shared order.
func scanPlanRowsPrefixed(rows *sql.Rows, keyID *string) (Plan, error) {
	var p Plan
	var hardCut int
	var created, updated string
	var allowedModelsStr string
	err := rows.Scan(
		keyID,
		&p.ID, &p.TenantID, &p.Name, &p.Description,
		&p.LimitMicros, &p.LimitTokens,
		&p.RPMLimit, &p.TPMLimit, &p.ConcurrencyLimit,
		&p.Period, &p.AlertPct, &hardCut, &allowedModelsStr, &created, &updated,
	)
	if err != nil {
		return Plan{}, err
	}
	p.HardCutoff = hardCut != 0
	p.CreatedAt = parsePlanTime(created)
	p.UpdatedAt = parsePlanTime(updated)
	p.AllowedModels = splitPlanModels(allowedModelsStr)
	return p, nil
}

// parsePlanTime parses an RFC3339 string into time.Time. An empty or
// malformed string returns the zero time, treated as "no timestamp".
func parsePlanTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// splitPlanModels splits a comma-separated allowlist into trimmed patterns.
func splitPlanModels(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, m := range parts {
		if m = strings.TrimSpace(m); m != "" {
			out = append(out, m)
		}
	}
	return out
}

// boolToInt converts a bool to 0/1 for SQLite INTEGER columns.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
