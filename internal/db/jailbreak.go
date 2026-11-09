package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// JailbreakDoc is the agents.md-style injection document: one system prompt
// prepended to every request routed through a model whose toggle is on.
// geminiSafety additionally switches the Gemini-native safety filters off via
// the official safetySettings field, from the same switch.
type JailbreakDoc struct {
	ID           string `json:"id,omitempty"`
	Name         string `json:"name,omitempty"`
	Prompt       string `json:"prompt"`
	GeminiSafety bool   `json:"geminiSafety"`
	// Source is the .md file the prompt was uploaded from, kept for display so
	// the operator can tell an uploaded agents.md from something typed inline.
	Source string `json:"source,omitempty"`
	// Kind labels the instruction-file convention a preset came from:
	// AGENTS, SOUL, PERSONA, LOAD and the rest are files a tool reads by
	// name rather than prompts about a model, so the label is what tells
	// them apart in the library. Empty means unlabelled.
	Kind string `json:"kind,omitempty"`
	// Keywords bind this preset to every model id that matches one of them,
	// across providers and combo legs alike, so one preset covers a model
	// family without arming each id. See jailbreak_keywords.go for the modes
	// and the precedence rules.
	Keywords []JailbreakKeyword `json:"keywords,omitempty"`
	// Excludes are the same rules read as a deny list: a model id matching one
	// of them is never injected with this preset, even when a keyword above
	// matches it. A global exclude list (see GetJailbreakExcludes) overrides
	Excludes []JailbreakKeyword `json:"excludes,omitempty"`
	// Order is the operator's arrangement position in the preset list. Zero
	// means "unsorted" and falls back to id order, so a preset written by an
	// older build (or by import) keeps its place without a migration.
	Order int `json:"order,omitempty"`
	// CreatedAt / UpdatedAt are RFC3339 UTC stamps the store fills on write.
	// They let the dashboard sort by newest or recently-touched, which is how
	// an operator finds the preset they just edited in a long library.
	CreatedAt string `json:"createdAt,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// JailbreakModelConfig holds the binding between a model and a preset.
type JailbreakModelConfig struct {
	Enabled  bool   `json:"enabled"`
	PresetID string `json:"presetId,omitempty"`
}

const (
	jailbreakScope       = "jailbreak"
	jailbreakDocKey      = "default"
	jailbreakDocPrefix   = "doc:"
	jailbreakModelPrefix = "model:"
	// jailbreakExcludeKey holds the global exclude list, which denies every
	// preset and every exact binding.
	jailbreakExcludeKey = "exclude"
)

// GetJailbreakDoc returns the default saved document, or zero values when none was
// saved yet — no doc means no injection anywhere.
func (r *Repo) GetJailbreakDoc() (*JailbreakDoc, error) {
	return r.GetJailbreakPreset("default")
}

func (r *Repo) SaveJailbreakDoc(d *JailbreakDoc) error {
	if d.ID == "" {
		d.ID = "default"
	}
	return r.SaveJailbreakPreset(d)
}

// GetJailbreakPreset loads a preset by ID. "default" checks both doc:default and default.
func (r *Repo) GetJailbreakPreset(id string) (*JailbreakDoc, error) {
	if id == "" || id == "default" {
		doc, err := r.getJailbreakDocByKey(jailbreakDocKey)
		if err == nil && doc.Prompt != "" {
			doc.ID = "default"
			if doc.Name == "" {
				doc.Name = "Default"
			}
			return doc, nil
		}
		doc, err = r.getJailbreakDocByKey(jailbreakDocPrefix + "default")
		if err == nil && doc.Prompt != "" {
			doc.ID = "default"
			if doc.Name == "" {
				doc.Name = "Default"
			}
			return doc, nil
		}
		return &JailbreakDoc{ID: "default", Name: "Default"}, nil
	}

	doc, err := r.getJailbreakDocByKey(jailbreakDocPrefix + id)
	if err != nil {
		return nil, err
	}
	doc.ID = id
	return doc, nil
}

func (r *Repo) getJailbreakDocByKey(key string) (*JailbreakDoc, error) {
	var raw string
	err := r.db.QueryRowContext(context.Background(),
		`SELECT value FROM kv WHERE scope = ? AND key = ? LIMIT 1`,
		jailbreakScope, key,
	).Scan(&raw)
	if err == sql.ErrNoRows {
		return &JailbreakDoc{}, nil
	}
	if err != nil {
		return nil, err
	}
	var d JailbreakDoc
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return nil, fmt.Errorf("parse jailbreak doc %s: %w", key, err)
	}
	return &d, nil
}

// SaveJailbreakPreset saves or updates a preset.
func (r *Repo) SaveJailbreakPreset(d *JailbreakDoc) error {
	if d == nil {
		return fmt.Errorf("nil jailbreak doc")
	}
	// Timestamps are store-owned: a caller that sends none gets "now", and an
	// existing preset keeps its original CreatedAt so "newest" stays truthful.
	now := time.Now().UTC().Format(time.RFC3339)
	if d.CreatedAt == "" {
		existing, err := r.getJailbreakDocByKey(jailbreakDocKey)
		if err == nil && existing != nil && existing.CreatedAt != "" {
			d.CreatedAt = existing.CreatedAt
		} else {
			d.CreatedAt = now
		}
	}
	d.UpdatedAt = now
	id := d.ID
	if id == "" {
		id = "default"
		d.ID = id
	}
	if d.Name == "" {
		if d.Source != "" {
			d.Name = d.Source
		} else {
			d.Name = id
		}
	}
	keywords, err := NormalizeJailbreakKeywords(d.Keywords)
	if err != nil {
		return err
	}
	d.Keywords = keywords
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	key := jailbreakDocPrefix + id
	if id == "default" {
		key = jailbreakDocKey
	}
	_, err = r.db.ExecContext(context.Background(),
		`INSERT INTO kv (scope, key, value) VALUES (?, ?, ?)
		 ON CONFLICT(scope, key) DO UPDATE SET value = excluded.value`,
		jailbreakScope, key, string(raw),
	)
	r.invalidateJailbreakRuleset()
	return err
}

// DeleteJailbreakPreset removes a preset by ID and unbinds every model armed to it.
func (r *Repo) DeleteJailbreakPreset(id string) error {
	if id == "" {
		return nil
	}
	keys := []string{jailbreakDocPrefix + id}
	if id == "default" {
		keys = append(keys, jailbreakDocKey)
	}
	for _, k := range keys {
		if _, err := r.db.ExecContext(context.Background(), `DELETE FROM kv WHERE scope = ? AND key = ?`, jailbreakScope, k); err != nil {
			return err
		}
	}

	// Automatically unbind every model armed to this preset.
	rows, err := r.db.QueryContext(context.Background(), `SELECT key, value FROM kv WHERE scope = ? AND key LIKE ?`,
		jailbreakScope, jailbreakModelPrefix+"%")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	var toDelete []string
	for rows.Next() {
		var k, raw string
		if err := rows.Scan(&k, &raw); err != nil {
			continue
		}
		var cfg JailbreakModelConfig
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			continue
		}
		targetPreset := cfg.PresetID
		if targetPreset == "" {
			targetPreset = "default"
		}
		if targetPreset == id {
			toDelete = append(toDelete, k)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, k := range toDelete {
		if _, err := r.db.ExecContext(context.Background(), `DELETE FROM kv WHERE scope = ? AND key = ?`, jailbreakScope, k); err != nil {
			return err
		}
	}

	r.invalidateJailbreakRuleset()
	return nil
}

// ListJailbreakPresets returns all available presets.
func (r *Repo) ListJailbreakPresets() ([]*JailbreakDoc, error) {
	rows, err := r.db.QueryContext(
		context.Background(),
		`SELECT key, value FROM kv WHERE scope = ? AND (key LIKE ? OR key = ?) ORDER BY key ASC`,
		jailbreakScope, jailbreakDocPrefix+"%", jailbreakDocKey,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var presets []*JailbreakDoc
	seenDefault := false

	for rows.Next() {
		var k, raw string
		if err := rows.Scan(&k, &raw); err != nil {
			return nil, err
		}
		d := parseJailbreakPresetRow(k, raw)
		if d == nil {
			continue
		}
		if k == jailbreakDocKey || k == jailbreakDocPrefix+jailbreakDocKey {
			if seenDefault {
				continue
			}
			seenDefault = true
		}
		presets = append(presets, d)
	}
	// The operator's arrangement wins; a preset with no stored order (zero)
	// sorts after every arranged one and falls back to id order, so an import
	// or an older build keeps a stable place without a migration.
	sort.SliceStable(presets, func(i, j int) bool {
		oi, oj := presets[i].Order, presets[j].Order
		if oi != oj {
			if oi == 0 {
				return false
			}
			if oj == 0 {
				return true
			}
			return oi < oj
		}
		return presets[i].ID < presets[j].ID
	})
	return presets, rows.Err()
}

// parseJailbreakPresetRow decodes one kv row into a preset with a normalized
// id, or nil when the row is not a preset. Both the "default" key and the
// "doc:default" key name the same preset.
func parseJailbreakPresetRow(k, raw string) *JailbreakDoc {
	var d JailbreakDoc
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return nil
	}
	switch {
	case k == jailbreakDocKey || k == jailbreakDocPrefix+jailbreakDocKey:
		d.ID = jailbreakDocKey
		if d.Name == "" {
			d.Name = "Default"
		}
	case strings.HasPrefix(k, jailbreakDocPrefix):
		d.ID = strings.TrimPrefix(k, jailbreakDocPrefix)
		if d.Name == "" {
			if d.Source != "" {
				d.Name = d.Source
			} else {
				d.Name = d.ID
			}
		}
	default:
		return nil
	}
	return &d
}

// JailbreakModelEnabled reads one model's toggle. The key is the model id as
// it appears in /v1/models and as the client sends it. A missing entry means
// off: injection is opt-in per model, never a side effect of existing.
func (r *Repo) JailbreakModelEnabled(modelID string) (bool, error) {
	cfg, err := r.GetJailbreakModelConfig(modelID)
	if err != nil {
		return false, err
	}
	return cfg.Enabled, nil
}

// GetJailbreakModelConfig reads one model's configuration.
func (r *Repo) GetJailbreakModelConfig(modelID string) (JailbreakModelConfig, error) {
	var raw string
	err := r.db.QueryRowContext(
		context.Background(),
		`SELECT value FROM kv WHERE scope = ? AND key = ? LIMIT 1`,
		jailbreakScope, jailbreakModelPrefix+modelID,
	).Scan(&raw)
	if err == sql.ErrNoRows {
		return JailbreakModelConfig{Enabled: false}, nil
	}
	if err != nil {
		return JailbreakModelConfig{}, err
	}
	var cfg JailbreakModelConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return JailbreakModelConfig{}, fmt.Errorf("parse jailbreak config for %s: %w", modelID, err)
	}
	if cfg.Enabled && cfg.PresetID == "" {
		cfg.PresetID = "default"
	}
	return cfg, nil
}

// SetJailbreakModel flips one model's toggle, defaulting preset to "default".
func (r *Repo) SetJailbreakModel(modelID string, enabled bool) error {
	return r.SetJailbreakModelPreset(modelID, "default", enabled)
}

// SetJailbreakModelPreset binds a preset to a model.
func (r *Repo) SetJailbreakModelPreset(modelID string, presetID string, enabled bool) error {
	if modelID == "" {
		return fmt.Errorf("empty model id")
	}
	// "off" (or an empty preset with enabled=false) is a stored state, not a
	// delete: the row stays listed with its toggle off, and only the explicit
	// delete path removes it. A binding that was never armed and is now turned
	// off is still written, so the list shows what the operator touched.
	if presetID == "" || presetID == "off" || presetID == "none" {
		presetID = ""
	}
	cfg := JailbreakModelConfig{
		Enabled:  enabled,
		PresetID: presetID,
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(context.Background(),
		`INSERT INTO kv (scope, key, value) VALUES (?, ?, ?)
		 ON CONFLICT(scope, key) DO UPDATE SET value = excluded.value`,
		jailbreakScope, jailbreakModelPrefix+modelID, string(raw),
	)
	if err == nil {
		r.invalidateJailbreakRuleset()
	}
	return err
}

// ListJailbreakModels returns the models whose toggles are on.
func (r *Repo) ListJailbreakModels() (map[string]bool, error) {
	cfgs, err := r.ListJailbreakModelConfigs()
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(cfgs))
	for k, c := range cfgs {
		if c.Enabled {
			out[k] = true
		}
	}
	return out, nil
}

// ListJailbreakModelConfigs returns all model configurations. Any models bound to
// a preset that no longer exists are cleaned up and unbound to off.
func (r *Repo) ListJailbreakModelConfigs() (map[string]JailbreakModelConfig, error) {
	validPresets := map[string]bool{}
	pRows, pErr := r.db.QueryContext(
		context.Background(),
		`SELECT key FROM kv WHERE scope = ? AND (key LIKE ? OR key = ?)`,
		jailbreakScope, jailbreakDocPrefix+"%", jailbreakDocKey,
	)
	if pErr == nil {
		defer func() { _ = pRows.Close() }()
		for pRows.Next() {
			var pk string
			if pRows.Scan(&pk) == nil {
				if pk == jailbreakDocKey || pk == jailbreakDocPrefix+"default" {
					validPresets["default"] = true
				} else if strings.HasPrefix(pk, jailbreakDocPrefix) {
					validPresets[pk[len(jailbreakDocPrefix):]] = true
				}
			}
		}
		// A partial read here would silently drop presets from the valid set,
		// which unbinds every model armed to them. Fail instead.
		if err := pRows.Err(); err != nil {
			return nil, fmt.Errorf("list jailbreak presets: %w", err)
		}
	}

	rows, err := r.db.QueryContext(
		context.Background(),
		`SELECT key, value FROM kv WHERE scope = ? AND key LIKE ?`,
		jailbreakScope, jailbreakModelPrefix+"%",
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]JailbreakModelConfig{}
	var orphaned []string
	for rows.Next() {
		var k, raw string
		if err := rows.Scan(&k, &raw); err != nil {
			continue
		}
		var cfg JailbreakModelConfig
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			continue // malformed row means off
		}
		if cfg.Enabled {
			if cfg.PresetID == "" {
				cfg.PresetID = "default"
			}
			if len(validPresets) > 0 && !validPresets[cfg.PresetID] {
				orphaned = append(orphaned, k)
				continue
			}
		}
		// A binding is listed whether it is on or off: turning one off is a
		// temporary state the operator toggles back, so it must stay visible.
		// Only the delete button removes a row.
		out[k[len(jailbreakModelPrefix):]] = cfg
	}
	for _, k := range orphaned {
		_, _ = r.db.ExecContext(context.Background(), `DELETE FROM kv WHERE scope = ? AND key = ?`, jailbreakScope, k)
	}
	if len(orphaned) > 0 {
		r.invalidateJailbreakRuleset()
	}
	return out, rows.Err()
}

// GetJailbreakDocForModel returns the resolved preset document for a single
// model id: an exact binding first, then a keyword rule. Requests that also
// carry forwarded leg ids (combo and fusion) resolve through
// ResolveJailbreakDoc, which takes every candidate id.
func (r *Repo) GetJailbreakDocForModel(modelID string) (*JailbreakDoc, error) {
	return r.ResolveJailbreakDoc(modelID)
}

// jailbreakUsagePrefix marks the per-preset injection counters. The key is
// "usage:<preset id>", the value a JSON int. A counter is written on the
// request path (one UPDATE per injected request) and read by the dashboard's
// preset list, so an operator can see which presets actually earn their keep.
const jailbreakUsagePrefix = "usage:"

// RecordJailbreakUsage bumps the injection counter for one preset. It is
// best-effort: a failed write must never fail the request that was already
// injected, so the error is swallowed by the caller's log line.
func (r *Repo) RecordJailbreakUsage(presetID string) error {
	if presetID == "" {
		return nil
	}
	key := jailbreakUsagePrefix + presetID
	var n int64
	row := r.db.QueryRowContext(context.Background(), `SELECT value FROM kv WHERE scope = ? AND key = ? LIMIT 1`, jailbreakScope, key)
	var raw string
	if err := row.Scan(&raw); err != nil && err != sql.ErrNoRows {
		return err
	}
	if raw != "" {
		_, _ = fmt.Sscanf(strings.TrimSpace(raw), "%d", &n)
	}
	n++
	val := strconv.FormatInt(n, 10)
	_, err := r.db.ExecContext(context.Background(),
		`INSERT INTO kv (scope, key, value) VALUES (?, ?, ?)
		 ON CONFLICT(scope, key) DO UPDATE SET value = excluded.value`,
		jailbreakScope, key, val,
	)
	return err
}

// JailbreakUsage returns the injection counter for every preset that has one,
// keyed by preset id. A preset with no counter is absent rather than zero, so
// the dashboard can tell "never used" from "counter cleared".
func (r *Repo) JailbreakUsage() (map[string]int64, error) {
	rows, err := r.db.QueryContext(context.Background(),
		`SELECT key, value FROM kv WHERE scope = ? AND key LIKE ?`,
		jailbreakScope, jailbreakUsagePrefix+"%",
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]int64)
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			return nil, err
		}
		id := strings.TrimPrefix(key, jailbreakUsagePrefix)
		n, _ := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		out[id] = n
	}
	return out, rows.Err()
}

// ReorderJailbreakPresets stores the operator's arrangement. order is the full
// preset id list in display order; a preset absent from it keeps its stored
// position, so a partial payload never resets the rest of the library.
func (r *Repo) ReorderJailbreakPresets(order []string) error {
	for i, id := range order {
		if id == "" {
			continue
		}
		key := jailbreakDocPrefix + id
		if id == jailbreakDocKey {
			key = jailbreakDocKey
		}
		doc, err := r.getJailbreakDocByKey(key)
		if err != nil {
			return err
		}
		if doc == nil {
			continue // an id that no longer exists is skipped, not an error
		}
		doc.Order = i + 1
		if err := r.SaveJailbreakPreset(doc); err != nil {
			return err
		}
	}
	return nil
}

// DeleteJailbreakModel removes one binding outright. Turning a binding off is a
// stored state the operator toggles back; this is the only path that drops the
// row, which is what the dashboard's delete button calls.
func (r *Repo) DeleteJailbreakModel(modelID string) error {
	if modelID == "" {
		return fmt.Errorf("empty model id")
	}
	_, err := r.db.ExecContext(context.Background(), `DELETE FROM kv WHERE scope = ? AND key = ?`,
		jailbreakScope, jailbreakModelPrefix+modelID)
	if err == nil {
		r.invalidateJailbreakRuleset()
	}
	return err
}
