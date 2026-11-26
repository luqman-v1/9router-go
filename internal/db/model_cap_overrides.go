package db

import (
	"database/sql"
	"encoding/json/jsontext"
	"fmt"
	"strings"
	"time"
)

// modelCapOverrideTable is declared in keiRouterTables and mirrored in
// postgresTables; these constants keep the readers and writers from drifting
// on the spelling.
const (
	modelCapOverrideTable       = "model_cap_overrides"
	modelCapOverrideProviderCol = "provider"
	modelCapOverrideModelCol    = "model"
	modelCapOverrideJSONCol     = "caps_override"
	modelCapOverrideUpdatedCol  = "updated_at"
)

// ModelCapOverrideRow is one stored sparse override.
type ModelCapOverrideRow struct {
	Provider string
	Model    string
	// CapsOverride is the raw JSON object as stored, so a caller that only
	// forwards it does not have to re-encode and risk reordering.
	CapsOverride jsontext.Value
}

// GetModelCapOverride returns the stored override for provider+model, or nil
// when the row does not exist. An empty/malformed stored object reads as nil:
// "nothing pinned" is the same answer as "no row", so callers need no special
// case and a corrupt row cannot pin garbage.
func (r *Repo) GetModelCapOverride(provider, model string) (*ModelCapOverrideRow, error) {
	if provider == "" || model == "" {
		return nil, nil
	}
	var raw string
	err := r.db.QueryRow( //nolint:noctx
		"SELECT "+modelCapOverrideJSONCol+" FROM "+modelCapOverrideTable+
			" WHERE "+modelCapOverrideProviderCol+" = ? AND "+modelCapOverrideModelCol+" = ? LIMIT 1",
		provider, model,
	).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get model caps override %s/%s: %w", provider, model, err)
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		return nil, nil
	}
	return &ModelCapOverrideRow{Provider: provider, Model: model, CapsOverride: jsontext.Value(trimmed)}, nil
}

// AllModelCapOverrides returns every stored override as "provider|model" ->
// raw JSON, which is the shape the capability loader consumes. A read error is
// returned rather than swallowed so the loader can fail open deliberately.
func (r *Repo) AllModelCapOverrides() (map[string]string, error) {
	rows, err := r.db.Query( //nolint:noctx
		"SELECT " + modelCapOverrideProviderCol + ", " + modelCapOverrideModelCol + ", " +
			modelCapOverrideJSONCol + " FROM " + modelCapOverrideTable,
	)
	if err != nil {
		return nil, fmt.Errorf("list model caps overrides: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var provider, model, raw string
		if err := rows.Scan(&provider, &model, &raw); err != nil {
			return nil, fmt.Errorf("scan model caps override: %w", err)
		}
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || trimmed == "{}" || trimmed == "null" {
			continue
		}
		out[provider+"|"+model] = trimmed
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate model caps overrides: %w", err)
	}
	return out, nil
}

// SetModelCapOverride upserts one override. A nil or empty object deletes the
// row, so "clear this model" needs no separate endpoint: the payload already
// says "nothing pinned".
func (r *Repo) SetModelCapOverride(provider, model string, override jsontext.Value) error {
	if provider == "" || model == "" {
		return fmt.Errorf("model caps override: provider and model are required")
	}
	trimmed := strings.TrimSpace(string(override))
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		return r.DeleteModelCapOverride(provider, model)
	}
	_, err := r.db.Exec( //nolint:noctx
		"INSERT INTO "+modelCapOverrideTable+" ("+modelCapOverrideProviderCol+", "+
			modelCapOverrideModelCol+", "+modelCapOverrideJSONCol+", "+modelCapOverrideUpdatedCol+
			") VALUES (?, ?, ?, ?) ON CONFLICT ("+modelCapOverrideProviderCol+", "+
			modelCapOverrideModelCol+") DO UPDATE SET "+modelCapOverrideJSONCol+" = excluded."+
			modelCapOverrideJSONCol+", "+modelCapOverrideUpdatedCol+" = excluded."+modelCapOverrideUpdatedCol,
		provider, model, trimmed, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("set model caps override %s/%s: %w", provider, model, err)
	}
	return nil
}

// DeleteModelCapOverride removes the row. Deleting an absent row is not an
// error: a clear on a model that never had an override is already the desired
// state.
func (r *Repo) DeleteModelCapOverride(provider, model string) error {
	if provider == "" || model == "" {
		return fmt.Errorf("model caps override: provider and model are required")
	}
	if _, err := r.db.Exec( //nolint:noctx
		"DELETE FROM "+modelCapOverrideTable+" WHERE "+modelCapOverrideProviderCol+
			" = ? AND "+modelCapOverrideModelCol+" = ?",
		provider, model,
	); err != nil {
		return fmt.Errorf("delete model caps override %s/%s: %w", provider, model, err)
	}
	return nil
}
