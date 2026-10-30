package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// PromptInjectionDoc represents one injection entry: a system prompt or a
// scripted chat that is stacked onto every request matching its target.
type PromptInjectionDoc struct {
	ID        string `json:"id,omitempty"`
	TargetType string `json:"targetType"` // "global" | "key" | "model" | "key-model"
	TargetID   string `json:"targetId,omitempty"` // empty for global
	Mode       string `json:"mode"`        // "system" | "script"
	Content    string `json:"content"`     // system prompt text or JSON script
	Enabled    bool   `json:"enabled"`
	CreatedAt  string `json:"createdAt,omitempty"`
	UpdatedAt  string `json:"updatedAt,omitempty"`
}

// PromptInjectionTemplate is a reusable template that copies into an injection.
type PromptInjectionTemplate struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name"`
	Content   string `json:"content"`
	Kind      string `json:"kind,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

const (
	promptInjectionScope       = "promptInjection"
	promptInjectionPrefix      = "injection:"
	promptInjectionEnabledKey  = "enabled"
	promptInjectionTemplatePrefix = "template:"
)

// GetPromptInjectionEnabled returns the master switch from the settings table.
func (r *Repo) GetPromptInjectionEnabled() bool {
	if r == nil {
		return false
	}
	s, err := r.GetSettings()
	if err != nil || s == nil {
		return false
	}
	return s.PromptInjectionEnabled
}

// SaveSettings persists the whole SettingsData row.
func (r *Repo) SaveSettings(s *SettingsData) error {
	if s == nil {
		return fmt.Errorf("nil settings")
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(context.Background(),
		`INSERT INTO settings (id, data) VALUES (1, ?)
		 ON CONFLICT(id) DO UPDATE SET data = excluded.data`,
		string(raw),
	)
	return err
}

// ListPromptInjectionTemplates returns all templates.
func (r *Repo) ListPromptInjectionTemplates() ([]*PromptInjectionTemplate, error) {
	rows, err := r.db.QueryContext(context.Background(),
		`SELECT key, value FROM kv WHERE scope = ? AND key LIKE ? ORDER BY key ASC`,
		promptInjectionScope, promptInjectionTemplatePrefix+"%",
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []*PromptInjectionTemplate
	for rows.Next() {
		var k, raw string
		if err := rows.Scan(&k, &raw); err != nil {
			return nil, err
		}
		var t PromptInjectionTemplate
		if err := json.Unmarshal([]byte(raw), &t); err != nil {
			continue
		}
		t.ID = strings.TrimPrefix(k, promptInjectionTemplatePrefix)
		out = append(out, &t)
	}
	return out, rows.Err()
}

// SavePromptInjectionTemplate creates or updates a template.
func (r *Repo) SavePromptInjectionTemplate(t *PromptInjectionTemplate) error {
	if t == nil {
		return fmt.Errorf("nil template")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if t.CreatedAt == "" {
		t.CreatedAt = now
	}
	t.UpdatedAt = now
	if t.ID == "" {
		t.ID = fmt.Sprintf("tpl-%d", time.Now().UnixNano())
	}
	if t.Name == "" {
		t.Name = t.ID
	}
	raw, err := json.Marshal(t)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(context.Background(),
		`INSERT INTO kv (scope, key, value) VALUES (?, ?, ?)
		 ON CONFLICT(scope, key) DO UPDATE SET value = excluded.value`,
		promptInjectionScope, promptInjectionTemplatePrefix+t.ID, string(raw),
	)
	return err
}

// DeletePromptInjectionTemplate removes a template.
func (r *Repo) DeletePromptInjectionTemplate(id string) error {
	if id == "" {
		return nil
	}
	_, err := r.db.ExecContext(context.Background(),
		`DELETE FROM kv WHERE scope = ? AND key = ?`,
		promptInjectionScope, promptInjectionTemplatePrefix+id,
	)
	return err
}

// ListPromptInjections returns all injections, optionally filtered by type.
func (r *Repo) ListPromptInjections(targetType string) ([]*PromptInjectionDoc, error) {
	var rows *sql.Rows
	var err error
	if targetType != "" {
		rows, err = r.db.QueryContext(context.Background(),
			`SELECT id, target_type, target_id, mode, content, enabled, created_at, updated_at
			 FROM promptInjections WHERE target_type = ? ORDER BY created_at ASC`,
			targetType,
		)
	} else {
		rows, err = r.db.QueryContext(context.Background(),
			`SELECT id, target_type, target_id, mode, content, enabled, created_at, updated_at
			 FROM promptInjections ORDER BY target_type ASC, target_id ASC, created_at ASC`,
		)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []*PromptInjectionDoc
	for rows.Next() {
		var d PromptInjectionDoc
		var enabledInt int
		if err := rows.Scan(&d.ID, &d.TargetType, &d.TargetID, &d.Mode, &d.Content, &enabledInt, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		d.Enabled = enabledInt != 0
		out = append(out, &d)
	}
	return out, rows.Err()
}

// GetPromptInjection returns one injection by id.
func (r *Repo) GetPromptInjection(id string) (*PromptInjectionDoc, error) {
	row := r.db.QueryRowContext(context.Background(),
		`SELECT id, target_type, target_id, mode, content, enabled, created_at, updated_at
		 FROM promptInjections WHERE id = ? LIMIT 1`,
		id,
	)
	var d PromptInjectionDoc
	var enabledInt int
	if err := row.Scan(&d.ID, &d.TargetType, &d.TargetID, &d.Mode, &d.Content, &enabledInt, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return nil, err
	}
	d.Enabled = enabledInt != 0
	return &d, nil
}

// SavePromptInjection creates or updates an injection.
func (r *Repo) SavePromptInjection(d *PromptInjectionDoc) error {
	if d == nil {
		return fmt.Errorf("nil injection")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if d.ID == "" {
		d.ID = fmt.Sprintf("pi-%d", time.Now().UnixNano())
	}
	if d.CreatedAt == "" {
		d.CreatedAt = now
	}
	d.UpdatedAt = now
	if d.TargetType == "" {
		d.TargetType = "global"
	}
	if d.TargetID == "" {
		d.TargetID = d.TargetType
	}
	if d.Mode == "" {
		d.Mode = "system"
	}

	enabledInt := 0
	if d.Enabled {
		enabledInt = 1
	}

	_, err := r.db.ExecContext(context.Background(),
		`INSERT INTO promptInjections (id, target_type, target_id, mode, content, enabled, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
			target_type = excluded.target_type,
			target_id = excluded.target_id,
			mode = excluded.mode,
			content = excluded.content,
			enabled = excluded.enabled,
			updated_at = excluded.updated_at`,
		d.ID, d.TargetType, d.TargetID, d.Mode, d.Content, enabledInt, d.CreatedAt, d.UpdatedAt,
	)
	return err
}

// DeletePromptInjection removes an injection.
func (r *Repo) DeletePromptInjection(id string) error {
	if id == "" {
		return nil
	}
	_, err := r.db.ExecContext(context.Background(),
		`DELETE FROM promptInjections WHERE id = ?`, id,
	)
	return err
}

// ResolvePromptInjection resolves the best matching injection for a request.
// Specificity order: key-model > key > model > global.
// Returns nil if none match or the feature is disabled.
func (r *Repo) ResolvePromptInjection(modelID string, keyID string) (*PromptInjectionDoc, error) {
	if r == nil || !r.GetPromptInjectionEnabled() {
		return nil, nil
	}

	// Build candidate keys in specificity order.
	candidates := []string{}
	if keyID != "" && modelID != "" {
		candidates = append(candidates, "key-model:"+keyID+":"+modelID)
	}
	if keyID != "" {
		candidates = append(candidates, "key:"+keyID)
	}
	if modelID != "" {
		candidates = append(candidates, "model:"+modelID)
	}
	candidates = append(candidates, "global:global")

	var best *PromptInjectionDoc
	for _, key := range candidates {
		row := r.db.QueryRowContext(context.Background(),
			`SELECT id, target_type, target_id, mode, content, enabled, created_at, updated_at
			 FROM promptInjections WHERE target_type = ? AND target_id = ? AND enabled = 1 LIMIT 1`,
			key[:strings.IndexByte(key, ':')], key[strings.IndexByte(key, ':')+1:],
		)
		var d PromptInjectionDoc
		var enabledInt int
		if err := row.Scan(&d.ID, &d.TargetType, &d.TargetID, &d.Mode, &d.Content, &enabledInt, &d.CreatedAt, &d.UpdatedAt); err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			return nil, err
		}
		d.Enabled = enabledInt != 0
		if d.Enabled && (best == nil || specificity(d.TargetType) > specificity(best.TargetType)) {
			best = &d
		}
	}
	return best, nil
}

func specificity(t string) int {
	switch t {
	case "key-model":
		return 4
	case "key":
		return 3
	case "model":
		return 2
	case "global":
		return 1
	}
	return 0
}
