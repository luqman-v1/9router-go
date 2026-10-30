package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"9router/proxy/internal/models"
)

// CliToolMapping represents a slot → model mapping for a specific CLI tool + API key.
type CliToolMapping struct {
	ID          string
	ToolID      string
	APIKeyID    string
	SlotKey     string
	TargetModel string
	SourceModel string
	Enabled     int
	CreatedAt   string
	UpdatedAt   string
}

// CliToolSettings holds per-key per-tool settings.
type CliToolSettings struct {
	APIKeyID        string
	ToolID          string
	MappingsEnabled int
	Mode            string
	CreatedAt       string
	UpdatedAt       string
}

// EnsureCliToolTables creates the CLI tool mapping tables if absent.
func EnsureCliToolTables(db *sql.DB) error {
	tables := []string{
		`CREATE TABLE IF NOT EXISTS cliToolMappings (
			id TEXT PRIMARY KEY,
			toolId TEXT NOT NULL,
			apiKeyId TEXT NOT NULL,
			slotKey TEXT NOT NULL,
			targetModel TEXT NOT NULL,
			sourceModel TEXT DEFAULT '',
			enabled INTEGER DEFAULT 1,
			createdAt TEXT DEFAULT '',
			updatedAt TEXT DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS cliToolSettings (
			apiKeyId TEXT NOT NULL,
			toolId TEXT NOT NULL,
			mappingsEnabled INTEGER DEFAULT 1,
			mode TEXT DEFAULT 'remote',
			createdAt TEXT DEFAULT '',
			updatedAt TEXT DEFAULT '',
			PRIMARY KEY (apiKeyId, toolId)
		)`,
	}
	for _, stmt := range tables {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("cli tool tables: %w", err)
		}
	}
	if _, err := db.Exec(
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_cli_tool_mappings_unique
		 ON cliToolMappings(toolId, apiKeyId, slotKey)`,
	); err != nil {
		return fmt.Errorf("cli tool mappings index: %w", err)
	}
	return nil
}

// GetCliToolMappings returns enabled mappings for a key+tool pair.
func (r *Repo) GetCliToolMappings(apiKeyID, toolID string) ([]CliToolMapping, error) {
	rows, err := r.db.Query(
		`SELECT id, toolId, apiKeyId, slotKey, targetModel, sourceModel, enabled, createdAt, updatedAt
		 FROM cliToolMappings
		 WHERE apiKeyId = ? AND toolId = ? AND enabled = 1
		 ORDER BY slotKey`,
		apiKeyID, toolID,
	)
	if err != nil {
		return nil, fmt.Errorf("get cli tool mappings: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []CliToolMapping
	for rows.Next() {
		var m CliToolMapping
		if err := rows.Scan(&m.ID, &m.ToolID, &m.APIKeyID, &m.SlotKey, &m.TargetModel, &m.SourceModel, &m.Enabled, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan cli tool mapping: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// UpsertCliToolMapping inserts or replaces a mapping row.
func (r *Repo) UpsertCliToolMapping(m CliToolMapping) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if m.ID == "" {
		m.ID = fmt.Sprintf("%s:%s:%s:%s", m.ToolID, m.APIKeyID, m.SlotKey, now)
	}
	_, err := r.db.Exec(
		`INSERT INTO cliToolMappings (id, toolId, apiKeyId, slotKey, targetModel, sourceModel, enabled, createdAt, updatedAt)
		 VALUES (?, ?, ?, ?, ?, ?, ?, COALESCE(?, ''), ?)
		 ON CONFLICT(id) DO UPDATE SET
			targetModel = excluded.targetModel,
			sourceModel = excluded.sourceModel,
			enabled = excluded.enabled,
			updatedAt = excluded.updatedAt`,
		m.ID, m.ToolID, m.APIKeyID, m.SlotKey, m.TargetModel, m.SourceModel, m.Enabled, m.CreatedAt, now,
	)
	if err != nil {
		return fmt.Errorf("upsert cli tool mapping: %w", err)
	}
	return nil
}

// DeleteCliToolMapping removes a mapping by id.
func (r *Repo) DeleteCliToolMapping(id string) error {
	_, err := r.db.Exec(`DELETE FROM cliToolMappings WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete cli tool mapping: %w", err)
	}
	return nil
}

// GetCliToolSettings returns settings for a key+tool pair.
func (r *Repo) GetCliToolSettings(apiKeyID, toolID string) (CliToolSettings, error) {
	var s CliToolSettings
	err := r.db.QueryRow(
		`SELECT apiKeyId, toolId, mappingsEnabled, mode, createdAt, updatedAt
		 FROM cliToolSettings
		 WHERE apiKeyId = ? AND toolId = ?`,
		apiKeyID, toolID,
	).Scan(&s.APIKeyID, &s.ToolID, &s.MappingsEnabled, &s.Mode, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CliToolSettings{APIKeyID: apiKeyID, ToolID: toolID, MappingsEnabled: 1, Mode: "remote"}, nil
		}
		return s, fmt.Errorf("get cli tool settings: %w", err)
	}
	return s, nil
}

// SetCliToolSettings upserts settings for a key+tool pair.
func (r *Repo) SetCliToolSettings(s CliToolSettings) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if s.CreatedAt == "" {
		s.CreatedAt = now
	}
	s.UpdatedAt = now
	_, err := r.db.Exec(
		`INSERT INTO cliToolSettings (apiKeyId, toolId, mappingsEnabled, mode, createdAt, updatedAt)
		 VALUES (?, ?, ?, ?, COALESCE(?, ''), ?)
		 ON CONFLICT(apiKeyId, toolId) DO UPDATE SET
			mappingsEnabled = excluded.mappingsEnabled,
			mode = excluded.mode,
			updatedAt = excluded.updatedAt`,
		s.APIKeyID, s.ToolID, s.MappingsEnabled, s.Mode, s.CreatedAt, s.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("set cli tool settings: %w", err)
	}
	return nil
}

// CliToolInfo describes a supported CLI tool for mapping.
type CliToolInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	UserAgent   string `json:"userAgent"`
	DefaultSlot string `json:"defaultSlot"`
}

// KnownCliTools returns the registry of CLI tools that support slot mapping.
func KnownCliTools() []CliToolInfo {
	return []CliToolInfo{
		{ID: "claude", Name: "Claude Code", UserAgent: "claude-cli/|claude-code/", DefaultSlot: "sonnet"},
	}
}

// DetectCliToolID returns the tool id if the User-Agent matches a known CLI tool, else "".
func DetectCliToolID(ua string) string {
	for _, tool := range KnownCliTools() {
		if tool.UserAgent != "" {
			parts := splitUA(tool.UserAgent)
			for _, p := range parts {
				if p != "" && containsUA(ua, p) {
					return tool.ID
				}
			}
		}
	}
	return ""
}

func splitUA(s string) []string {
	var out []string
	cur := ""
	for _, c := range s {
		if c == '|' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		cur += string(c)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func containsUA(ua, part string) bool {
	idx := strings.Index(ua, part)
	if idx < 0 {
		return false
	}
	// ensure boundary: start or non-alpha before, and / or space or digit after
	// (version strings like claude-cli/1.0.0 end with digit, not /)
	before := idx == 0 || !((ua[idx-1] >= 'a' && ua[idx-1] <= 'z') || (ua[idx-1] >= 'A' && ua[idx-1] <= 'Z'))
	afterPos := idx + len(part)
	after := afterPos >= len(ua) ||
		ua[afterPos] == '/' || ua[afterPos] == ' ' ||
		(ua[afterPos] >= '0' && ua[afterPos] <= '9') ||
		ua[afterPos] == '.'
	return before && after
}

// ResolveCliModel returns the mapped target model for a key+tool+slot, or "" if no mapping.
func (r *Repo) ResolveCliModel(apiKeyID, toolID, slot string) (string, error) {
	if apiKeyID == "" || toolID == "" || slot == "" {
		return "", nil
	}
	settings, err := r.GetCliToolSettings(apiKeyID, toolID)
	if err != nil {
		return "", err
	}
	if settings.MappingsEnabled != 1 || settings.Mode != "remote" {
		return "", nil
	}
	mappings, err := r.GetCliToolMappings(apiKeyID, toolID)
	if err != nil {
		return "", err
	}
	for _, m := range mappings {
		if m.SlotKey == slot {
			return m.TargetModel, nil
		}
	}
	return "", nil
}

// HasCliMappingScope checks whether an APIKey carries the routing:cli_mapping scope.
func HasCliMappingScope(key *models.APIKey) bool {
	if key == nil {
		return false
	}
	for _, s := range key.Scopes {
		if s == "routing:cli_mapping" {
			return true
		}
	}
	return false
}
