package db

import (
	"database/sql"
	"fmt"
)

// promptInjectionTables are Go-only tables backing the Prompt Injection Pro
// feature (system prompt or chat script per global / API key / model / combo).
var promptInjectionTables = []tableDef{
	{
		name: "promptInjections",
		columns: [][2]string{
			{"id", "TEXT PRIMARY KEY"},
			{"target_type", "TEXT NOT NULL"},
			{"target_id", "TEXT NOT NULL"},
			{"mode", "TEXT NOT NULL"},
			{"content", "TEXT NOT NULL"},
			{"enabled", "INTEGER NOT NULL DEFAULT 1"},
			{"created_at", "TEXT NOT NULL"},
			{"updated_at", "TEXT NOT NULL"},
		},
		indexes: []string{
			"CREATE UNIQUE INDEX IF NOT EXISTS idx_pi_target ON promptInjections(target_type, target_id)",
			"CREATE INDEX IF NOT EXISTS idx_pi_enabled ON promptInjections(enabled)",
		},
	},
	{
		name: "promptInjectionTemplates",
		columns: [][2]string{
			{"id", "TEXT PRIMARY KEY"},
			{"name", "TEXT NOT NULL"},
			{"content", "TEXT NOT NULL"},
			{"kind", "TEXT DEFAULT ''"},
			{"created_at", "TEXT NOT NULL"},
			{"updated_at", "TEXT NOT NULL"},
		},
	},
}

// EnsurePromptInjectionTables creates the Go-only tables the Prompt Injection
// Pro feature needs. Exported so test fixtures that only build the upstream
// core schema still get the full runtime shape.
func EnsurePromptInjectionTables(db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("prompt injection tables: nil db")
	}
	for _, t := range promptInjectionTables {
		if err := ensureTable(db, t); err != nil {
			return err
		}
	}
	return nil
}
