package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Per-API-key model access control (F-7).
//
// api_key_model_access holds one row per (key, model-pattern). It is pure
// policy: it never influences which provider serves a model (AGENTS.md §3).
//
// An empty result set means "no allowlist configured" and therefore allows
// everything, so every key created before this feature keeps behaving exactly
// as it did before.

const modelAccessTable = "api_key_model_access"

// GetAllowedModels returns the model's allowlist patterns for one API key.
// The returned slice is empty (never nil) when the key has no allowlist, which
// callers read as "allow all".
func (r *Repo) GetAllowedModels(apiKeyID string) ([]string, error) {
	if r == nil || r.db == nil || apiKeyID == "" {
		return []string{}, nil
	}
	rows, err := r.db.Query(
		`SELECT model FROM `+modelAccessTable+` WHERE api_key_id = ? ORDER BY model`,
		apiKeyID,
	)
	if err != nil {
		return nil, fmt.Errorf("get allowed models %s: %w", apiKeyID, err)
	}
	defer rows.Close()

	out := make([]string, 0, 8)
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, fmt.Errorf("get allowed models %s: scan: %w", apiKeyID, err)
		}
		if m = strings.TrimSpace(m); m != "" {
			out = append(out, m)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get allowed models %s: %w", apiKeyID, err)
	}
	return out, nil
}

// GetAllowedModelsForKeys returns the allowlist of every id in one query, so a
// dashboard listing of N keys costs 1 query instead of N. Keys without a row
// are present in the map with an empty slice, keeping the caller's
// "absent == allow all" rule uniform.
func (r *Repo) GetAllowedModelsForKeys(ids []string) (map[string][]string, error) {
	out := make(map[string][]string, len(ids))
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
		out[id] = []string{}
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	if len(placeholders) == 0 {
		return out, nil
	}

	query := `SELECT api_key_id, model FROM ` + modelAccessTable +
		` WHERE api_key_id IN (` + strings.Join(placeholders, ",") + `) ORDER BY api_key_id, model`
	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("get allowed models for keys: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var keyID, model string
		if err := rows.Scan(&keyID, &model); err != nil {
			return nil, fmt.Errorf("get allowed models for keys: scan: %w", err)
		}
		if model = strings.TrimSpace(model); model != "" {
			out[keyID] = append(out[keyID], model)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get allowed models for keys: %w", err)
	}
	return out, nil
}

// SetAllowedModels replaces a key's allowlist in a single transaction: either
// the whole new list lands or none of it does, so a client can never observe a
// half-applied policy. Passing an empty list clears the allowlist and restores
// the allow-everything default.
func (r *Repo) SetAllowedModels(apiKeyID string, models []string) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("set allowed models %s: nil repo", apiKeyID)
	}
	if apiKeyID == "" {
		return fmt.Errorf("set allowed models: empty apiKey id")
	}

	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("set allowed models %s: begin transaction: %w", apiKeyID, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit

	if _, err := tx.Exec(`DELETE FROM `+modelAccessTable+` WHERE api_key_id = ?`, apiKeyID); err != nil {
		return fmt.Errorf("set allowed models %s: clear: %w", apiKeyID, err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	seen := make(map[string]bool, len(models))
	for _, m := range models {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		if _, err := tx.Exec(
			`INSERT INTO `+modelAccessTable+` (api_key_id, model, created_at) VALUES (?, ?, ?)`,
			apiKeyID, m, now,
		); err != nil {
			return fmt.Errorf("set allowed models %s: insert %q: %w", apiKeyID, m, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("set allowed models %s: commit: %w", apiKeyID, err)
	}
	return nil
}

// HasAllowedModelsTable reports whether the per-key access table exists. A
// database created before F-7 answers false, which keeps a missing table a
// "no allowlist" case instead of a startup failure.
func (r *Repo) HasAllowedModelsTable() bool {
	if r == nil || r.db == nil {
		return false
	}
	var name string
	err := r.db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ? LIMIT 1`,
		modelAccessTable,
	).Scan(&name)
	if err == sql.ErrNoRows {
		return false
	}
	return err == nil && name == modelAccessTable
}