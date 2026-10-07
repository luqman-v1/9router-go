package db

import (
	json "encoding/json/v2"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"9router/proxy/internal/providers"
)

// deprecationScope is the kv scope the gateway files model deprecations under.
// It is kv rather than a table because the set is small, entirely machine
// written, and read as a whole map on every dashboard load — the same shape as
// the operator-managed modelAliases/customModels scopes next to it.
const deprecationScope = "modelDeprecations"

// ErrModelNotDeprecated is returned when a model carries no recorded
// deprecation. Callers treat it as "unknown", never as "alive".
var ErrModelNotDeprecated = errors.New("model is not deprecated")

// RecordModelDeprecation files what is known about a model that has gone away
// upstream. It is an upsert on purpose: a second 410 with a better message
// replaces the first, and a model that was retired by a catalogue sync and
// then confirmed by a live 410 keeps the strongest reading.
func (r *Repo) RecordModelDeprecation(dep providers.ModelDeprecation) error {
	if dep.Provider == "" || dep.Model == "" || dep.Status == providers.DeprecationUnknown {
		return fmt.Errorf("record model deprecation: provider, model and status are required")
	}
	if dep.DetectedAt == "" {
		dep.DetectedAt = time.Now().UTC().Format(time.RFC3339)
	}
	encoded, err := json.Marshal(dep)
	if err != nil {
		return fmt.Errorf("record model deprecation: %w", err)
	}
	key := providers.DeprecationKey(dep.Provider, dep.Model)
	if err := r.SetKV(deprecationScope, key, string(encoded)); err != nil {
		return fmt.Errorf("record model deprecation: %w", err)
	}
	return nil
}

// GetModelDeprecation returns what is recorded for one provider/model pair.
func (r *Repo) GetModelDeprecation(provider, model string) (*providers.ModelDeprecation, error) {
	var raw string
	err := r.db.QueryRow(
		"SELECT value FROM kv WHERE scope = ? AND key = ? LIMIT 1",
		deprecationScope, providers.DeprecationKey(provider, model),
	).Scan(&raw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrModelNotDeprecated
		}
		return nil, fmt.Errorf("get model deprecation %s/%s: %w", provider, model, err)
	}
	var dep providers.ModelDeprecation
	if err := json.Unmarshal([]byte(raw), &dep); err != nil {
		// A row this build cannot read is worse than no row: the dashboard
		// would render a model as healthy on the strength of a parse failure.
		return nil, fmt.Errorf("get model deprecation %s/%s: %w", provider, model, err)
	}
	return &dep, nil
}

// ListModelDeprecations returns every recorded deprecation, optionally scoped
// to one provider ("" for all). The rows are read as a map keyed by the
// "<provider>/<model>" key so the dashboard can look up a whole model list in
// one pass.
func (r *Repo) ListModelDeprecations(provider string) (map[string]providers.ModelDeprecation, error) {
	rows, err := r.db.Query(
		"SELECT key, value FROM kv WHERE scope = ? ORDER BY key", deprecationScope,
	)
	if err != nil {
		return nil, fmt.Errorf("list model deprecations: %w", err)
	}
	defer rows.Close()

	out := make(map[string]providers.ModelDeprecation)
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			return nil, fmt.Errorf("list model deprecations: %w", err)
		}
		var dep providers.ModelDeprecation
		if err := json.Unmarshal([]byte(raw), &dep); err != nil {
			continue
		}
		if dep.Provider == "" || dep.Model == "" {
			continue
		}
		if provider != "" && dep.Provider != provider {
			continue
		}
		out[key] = dep
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list model deprecations: %w", err)
	}
	return out, nil
}

// ClearModelDeprecation forgets one model. A served request calls it so a
// provider that brought a model back stops showing a stale badge.
func (r *Repo) ClearModelDeprecation(provider, model string) error {
	if err := r.DeleteKV(deprecationScope, providers.DeprecationKey(provider, model)); err != nil {
		return fmt.Errorf("clear model deprecation: %w", err)
	}
	return nil
}

// RetainLiveModels drops the retired records of one provider whose model still
// appears in a freshly fetched catalogue, and returns how many it dropped.
//
// It is deliberately not the mirror image of marking absences deprecated: a
// catalogue is frequently partial (an OpenAI-compatible node behind a
// feature flag, a paginated feed), so a missing id is not evidence of death.
// Confirming liveness is evidence of life, and that is all this does.
func (r *Repo) RetainLiveModels(provider string, live map[string]bool) (int, error) {
	deps, err := r.ListModelDeprecations(provider)
	if err != nil {
		return 0, err
	}
	cleared := 0
	for _, dep := range deps {
		if !live[providers.DeprecationKey(provider, dep.Model)] {
			continue
		}
		if err := r.ClearModelDeprecation(provider, dep.Model); err != nil {
			return cleared, err
		}
		cleared++
	}
	return cleared, nil
}