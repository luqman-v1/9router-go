package db

import (
	"context"
	"database/sql"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"time"

	"9router/proxy/internal/models"
)

const pruneComboBatchSize = 200

// PruneComboModels removes entries matching shouldDrop from every combo's models
// list inside one transaction using batched UPDATE statements. Untouched combos
// keep their exact row JSON and updatedAt untouched. Returns the number of
// updated combos.
func (r *Repo) PruneComboModels(ctx context.Context, shouldDrop func(entry string) bool) (int, error) {
	if shouldDrop == nil {
		return 0, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("prune combo models: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	updated, err := pruneComboModelsTx(ctx, tx, shouldDrop)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("prune combo models: commit: %w", err)
	}
	return updated, nil
}

// pruneComboModelsTx scans combos.models once, drops entries matched by
// shouldDrop while preserving raw element shapes (strings and weighted objects),
// and applies updates in batched CASE/IN statements.
func pruneComboModelsTx(ctx context.Context, tx *sql.Tx, shouldDrop func(entry string) bool) (int, error) {
	if shouldDrop == nil {
		return 0, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, models FROM combos`)
	if err != nil {
		return 0, fmt.Errorf("prune combo models: read combos: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type comboUpdate struct {
		id     string
		models string
	}
	var updates []comboUpdate
	for rows.Next() {
		var id, rawModels string
		if err := rows.Scan(&id, &rawModels); err != nil {
			return 0, fmt.Errorf("prune combo models: scan combo: %w", err)
		}
		rewritten, changed := filterComboModelsJSON(rawModels, shouldDrop)
		if changed {
			updates = append(updates, comboUpdate{id: id, models: rewritten})
		}
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("prune combo models: iterate combos: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("prune combo models: close combos: %w", err)
	}
	if len(updates) == 0 {
		return 0, nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	for start := 0; start < len(updates); start += pruneComboBatchSize {
		end := start + pruneComboBatchSize
		if end > len(updates) {
			end = len(updates)
		}
		batch := updates[start:end]

		var sb strings.Builder
		args := make([]any, 0, len(batch)*3+1)
		sb.WriteString(`UPDATE combos SET models = CASE id`)
		for _, u := range batch {
			sb.WriteString(` WHEN ? THEN ?`)
			args = append(args, u.id, u.models)
		}
		sb.WriteString(` END, updatedAt = ? WHERE id IN (`)
		args = append(args, now)
		for i, u := range batch {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteByte('?')
			args = append(args, u.id)
		}
		sb.WriteByte(')')

		if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
			return 0, fmt.Errorf("prune combo models: batch update: %w", err)
		}
	}
	return len(updates), nil
}

// filterComboModelsJSON removes elements matched by shouldDrop while preserving
// both plain-string and weighted-object elements verbatim.
func filterComboModelsJSON(rawModels string, shouldDrop func(entry string) bool) (string, bool) {
	if strings.TrimSpace(rawModels) == "" {
		return rawModels, false
	}
	var elems []jsontext.Value
	if err := json.Unmarshal([]byte(rawModels), &elems); err != nil {
		return rawModels, false
	}
	kept := make([]jsontext.Value, 0, len(elems))
	changed := false
	for _, elem := range elems {
		var cm models.ComboModel
		if err := json.Unmarshal(elem, &cm); err == nil && cm.Model != "" && shouldDrop(cm.Model) {
			changed = true
			continue
		}
		kept = append(kept, elem)
	}
	if !changed {
		return rawModels, false
	}
	encoded, err := json.Marshal(kept)
	if err != nil {
		return rawModels, false
	}
	return string(encoded), true
}

// ProviderActiveStatus returns whether each provider that has stored rows in
// providerConnections has at least one active (isActive = 1) connection.
// Providers with zero connection rows are absent from the returned map.
func (r *Repo) ProviderActiveStatus(ctx context.Context) (map[string]bool, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT provider, COALESCE(MAX(isActive), 0) FROM providerConnections GROUP BY provider`)
	if err != nil {
		return nil, fmt.Errorf("provider active status: %w", err)
	}
	defer func() { _ = rows.Close() }()

	status := make(map[string]bool)
	for rows.Next() {
		var prov string
		var maxActive int
		if err := rows.Scan(&prov, &maxActive); err != nil {
			return nil, fmt.Errorf("provider active status: scan: %w", err)
		}
		if prov != "" {
			status[prov] = maxActive > 0
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("provider active status: %w", err)
	}
	return status, nil
}

// DeleteProviderConnectionWithCount atomically deletes a provider connection by id
// and returns its provider name plus the count of remaining connections for that provider.
func (r *Repo) DeleteProviderConnectionWithCount(ctx context.Context, id string) (provider string, remaining int, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", 0, fmt.Errorf("delete connection: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var prov sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT provider FROM providerConnections WHERE id = ?`, id).Scan(&prov)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, nil
	}
	if err != nil {
		return "", 0, fmt.Errorf("delete connection: lookup provider: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM providerConnections WHERE id = ?`, id); err != nil {
		return "", 0, fmt.Errorf("delete connection: %w", err)
	}

	if prov.String != "" {
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM providerConnections WHERE provider = ?`, prov.String).Scan(&remaining); err != nil {
			return "", 0, fmt.Errorf("delete connection: count remaining: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return "", 0, fmt.Errorf("delete connection: commit: %w", err)
	}
	return strings.TrimSpace(prov.String), remaining, nil
}
