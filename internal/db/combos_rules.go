package db

import (
	"context"
	"database/sql"
	json "encoding/json/v2"
	"fmt"
	"strings"
	"time"

	"9router/proxy/internal/combos/smart"
)

// The smart-rule column and the methods that read and write it.
//
// A rule list lives in its own additive column rather than inside `models`:
// the models column is the wire contract (a plain array of id strings that
// comboModels unmarshals), and a rule is bookkeeping the resolver never reads.
// Keeping them apart is what lets the resolver, /v1/models and the capacity
// adapter stay untouched by this feature.

// rulesColumn is the additive column holding one combo's smart rules.
const rulesColumn = "matchRules"

// ComboIDsWithRules returns the ids of every combo carrying at least one rule.
//
// It is the early-out a caller makes before doing any work at all: an
// installation with no rules pays one scan of a small table and nothing else,
// so saving a model stays as cheap as it was before this feature existed.
func (r *Repo) ComboIDsWithRules(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id FROM combos WHERE `+rulesColumn+` IS NOT NULL AND `+rulesColumn+
			` NOT IN ('', '[]', 'null')`,
	)
	if err != nil {
		return nil, fmt.Errorf("combos with rules: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan combo id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("combos with rules: %w", err)
	}
	return ids, nil
}

// GetComboRules returns the raw rule column of one combo. A missing row and a
// row with no rules both return "".
func (r *Repo) GetComboRules(ctx context.Context, id string) (string, error) {
	var raw string
	err := r.db.QueryRowContext(ctx,
		`SELECT `+rulesColumn+` FROM combos WHERE id = ? LIMIT 1`, id,
	).Scan(&raw)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get combo rules %s: %w", id, err)
	}
	return raw, nil
}

// ComboRulesByID returns the raw rule column of every combo, keyed by combo id.
// Combos without rules are absent from the map rather than present and empty.
func (r *Repo) ComboRulesByID(ctx context.Context) (map[string]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, `+rulesColumn+` FROM combos`)
	if err != nil {
		return nil, fmt.Errorf("combo rules by id: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]string)
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, fmt.Errorf("scan combo rules: %w", err)
		}
		if !smart.HasRules(raw) {
			continue
		}
		out[id] = raw
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("combo rules by id: %w", err)
	}
	return out, nil
}

// SetComboRules replaces the rule list of one combo.
func (r *Repo) SetComboRules(ctx context.Context, id, raw string) error {
	if _, err := r.db.ExecContext(ctx, `UPDATE combos SET `+rulesColumn+` = ? WHERE id = ?`, raw, id); err != nil {
		return fmt.Errorf("set combo rules %s: %w", id, err)
	}
	return nil
}

// ReconcileCombo applies reconcile to one combo's membership inside a single
// write transaction, and reports whether anything changed.
//
// The read and the write share one transaction on purpose. A reconcile is a
// read-modify-write, and the dashboard's own PUT /api/combos/{id} writes the
// same two columns: without the transaction a reconcile that read a row, lost
// the race to an operator edit and then wrote its stale answer would silently
// drop the operator's change.
//
// reconcile must not touch the database. It runs while the write lock is held
// and the pool is small (MaxOpenConns is 4), so a query inside it can deadlock
// the whole gateway; the caller builds its candidate list before calling in.
//
// A combo that no longer exists is not an error: the row was deleted between
// the caller's listing and this call, and there is nothing left to reconcile.
func (r *Repo) ReconcileCombo(
	ctx context.Context,
	id string,
	reconcile func(models []string, rules []smart.Rule) ([]string, []smart.Rule, bool),
) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("reconcile combo %s: begin: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()

	var modelsJSON, rulesJSON string
	if err := tx.QueryRowContext(ctx,
		`SELECT models, `+rulesColumn+` FROM combos WHERE id = ?`, id,
	).Scan(&modelsJSON, &rulesJSON); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, fmt.Errorf("reconcile combo %s: read: %w", id, err)
	}

	var models []string
	if strings.TrimSpace(modelsJSON) != "" {
		if err := json.Unmarshal([]byte(modelsJSON), &models); err != nil {
			return false, fmt.Errorf("reconcile combo %s: models unreadable: %w", id, err)
		}
	}
	rules, err := smart.ParseRules(rulesJSON)
	if err != nil {
		return false, fmt.Errorf("reconcile combo %s: %w", id, err)
	}

	newModels, newRules, changed := reconcile(models, rules)
	if !changed {
		return false, nil
	}

	encodedModels, err := json.Marshal(newModels)
	if err != nil {
		return false, fmt.Errorf("reconcile combo %s: encode models: %w", id, err)
	}
	encodedRules, err := smart.EncodeRules(newRules)
	if err != nil {
		return false, fmt.Errorf("reconcile combo %s: encode rules: %w", id, err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.ExecContext(ctx,
		`UPDATE combos SET models = ?, `+rulesColumn+` = ?, updatedAt = ? WHERE id = ?`,
		string(encodedModels), encodedRules, now, id,
	); err != nil {
		return false, fmt.Errorf("reconcile combo %s: write: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("reconcile combo %s: commit: %w", id, err)
	}
	return true, nil
}
