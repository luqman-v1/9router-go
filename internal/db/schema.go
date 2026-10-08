package db

import (
	"database/sql"
	"fmt"
	"strings"
)

// tableDef mirrors one entry of upstream schema.js TABLES: the declarative
// current schema consumed by syncSchemaFromTables(). Column definitions are
// kept verbatim so a Go-bootstrapped database stays readable/writable by the
// upstream Next.js application and vice versa.
type tableDef struct {
	name       string
	columns    [][2]string // {name, definition}
	primaryKey string      // extra table-level constraint, e.g. PRIMARY KEY (scope, key)
	indexes    []string
}

// coreSchema declares the upstream v0.5.85 core tables in dependency-free
// order. It is additive-only: EnsureCoreSchema never drops, renames, or
// retypes anything, matching the upstream auto-sync contract.
func coreSchema() []tableDef {
	return []tableDef{
		{
			name: "settings",
			columns: [][2]string{
				{"id", "INTEGER PRIMARY KEY CHECK (id = 1)"},
				{"data", "TEXT NOT NULL"},
			},
		},
		{
			name: "_meta",
			columns: [][2]string{
				{"key", "TEXT PRIMARY KEY"},
				{"value", "TEXT NOT NULL"},
			},
		},
		{
			name: "providerConnections",
			columns: [][2]string{
				{"id", "TEXT PRIMARY KEY"},
				{"provider", "TEXT NOT NULL"},
				{"authType", "TEXT NOT NULL"},
				{"name", "TEXT"},
				{"email", "TEXT"},
				{"priority", "INTEGER"},
				{"isActive", "INTEGER DEFAULT 1"},
				{"data", "TEXT NOT NULL"},
				{"createdAt", "TEXT NOT NULL"},
				{"updatedAt", "TEXT NOT NULL"},
			},
			indexes: []string{
				"CREATE INDEX IF NOT EXISTS idx_pc_provider ON providerConnections(provider)",
				"CREATE INDEX IF NOT EXISTS idx_pc_provider_active ON providerConnections(provider, isActive)",
				"CREATE INDEX IF NOT EXISTS idx_pc_priority ON providerConnections(provider, priority)",
			},
		},
		{
			name: "providerNodes",
			columns: [][2]string{
				{"id", "TEXT PRIMARY KEY"},
				{"type", "TEXT"},
				{"name", "TEXT"},
				{"data", "TEXT NOT NULL"},
				{"createdAt", "TEXT NOT NULL"},
				{"updatedAt", "TEXT NOT NULL"},
			},
			indexes: []string{
				"CREATE INDEX IF NOT EXISTS idx_pn_type ON providerNodes(type)",
			},
		},
		{
			name: "proxyPools",
			columns: [][2]string{
				{"id", "TEXT PRIMARY KEY"},
				{"isActive", "INTEGER DEFAULT 1"},
				{"testStatus", "TEXT"},
				{"data", "TEXT NOT NULL"},
				{"createdAt", "TEXT NOT NULL"},
				{"updatedAt", "TEXT NOT NULL"},
			},
			indexes: []string{
				"CREATE INDEX IF NOT EXISTS idx_pp_active ON proxyPools(isActive)",
				"CREATE INDEX IF NOT EXISTS idx_pp_status ON proxyPools(testStatus)",
			},
		},
		{
			name: "apiKeys",
			columns: [][2]string{
				{"id", "TEXT PRIMARY KEY"},
				{"key", "TEXT UNIQUE NOT NULL"},
				{"name", "TEXT"},
				{"machineId", "TEXT"},
				{"isActive", "INTEGER DEFAULT 1"},
				{"createdAt", "TEXT NOT NULL"},
			},
			indexes: []string{
				"CREATE INDEX IF NOT EXISTS idx_ak_key ON apiKeys(key)",
			},
		},
		{
			name: "combos",
			columns: [][2]string{
				{"id", "TEXT PRIMARY KEY"},
				{"name", "TEXT UNIQUE NOT NULL"},
				{"kind", "TEXT"},
				{"models", "TEXT NOT NULL"},
				{"createdAt", "TEXT NOT NULL"},
				{"updatedAt", "TEXT NOT NULL"},
			},
			indexes: []string{
				"CREATE INDEX IF NOT EXISTS idx_combo_name ON combos(name)",
			},
		},
		{
			name: "kv",
			columns: [][2]string{
				{"scope", "TEXT NOT NULL"},
				{"key", "TEXT NOT NULL"},
				{"value", "TEXT NOT NULL"},
			},
			primaryKey: "PRIMARY KEY (scope, key)",
			indexes: []string{
				"CREATE INDEX IF NOT EXISTS idx_kv_scope ON kv(scope)",
			},
		},
		{
			name: "usageHistory",
			columns: [][2]string{
				{"id", "INTEGER PRIMARY KEY AUTOINCREMENT"},
				{"timestamp", "TEXT NOT NULL"},
				{"provider", "TEXT"},
				{"model", "TEXT"},
				{"connectionId", "TEXT"},
				{"apiKey", "TEXT"},
				{"endpoint", "TEXT"},
				{"promptTokens", "INTEGER DEFAULT 0"},
				{"completionTokens", "INTEGER DEFAULT 0"},
				{"cost", "REAL DEFAULT 0"},
				{"status", "TEXT"},
				{"tokens", "TEXT"},
				{"meta", "TEXT"},
			},
			indexes: []string{
				"CREATE INDEX IF NOT EXISTS idx_uh_ts ON usageHistory(timestamp DESC)",
				"CREATE INDEX IF NOT EXISTS idx_uh_provider ON usageHistory(provider)",
				"CREATE INDEX IF NOT EXISTS idx_uh_model ON usageHistory(model)",
				"CREATE INDEX IF NOT EXISTS idx_uh_conn ON usageHistory(connectionId)",
				"CREATE INDEX IF NOT EXISTS idx_uh_ts_prov ON usageHistory(timestamp DESC, provider)",
				"CREATE INDEX IF NOT EXISTS idx_uh_ts_model ON usageHistory(timestamp DESC, model)",
			},
		},
		{
			name: "usageDaily",
			columns: [][2]string{
				{"dateKey", "TEXT PRIMARY KEY"},
				{"data", "TEXT NOT NULL"},
			},
		},
		{
			name: "requestDetails",
			columns: [][2]string{
				{"id", "TEXT PRIMARY KEY"},
				{"timestamp", "TEXT NOT NULL"},
				{"provider", "TEXT"},
				{"model", "TEXT"},
				{"connectionId", "TEXT"},
				{"status", "TEXT"},
				{"data", "TEXT NOT NULL"},
			},
			indexes: []string{
				"CREATE INDEX IF NOT EXISTS idx_rd_ts ON requestDetails(timestamp DESC)",
				"CREATE INDEX IF NOT EXISTS idx_rd_provider ON requestDetails(provider)",
				"CREATE INDEX IF NOT EXISTS idx_rd_model ON requestDetails(model)",
				"CREATE INDEX IF NOT EXISTS idx_rd_conn ON requestDetails(connectionId)",
			},
		},
		{
			name: "compressionAnalytics",
			columns: [][2]string{
				{"id", "INTEGER PRIMARY KEY AUTOINCREMENT"},
				{"timestamp", "TEXT NOT NULL"},
				{"provider", "TEXT"},
				{"model", "TEXT"},
				{"mode", "TEXT NOT NULL"},
				{"originalTokens", "INTEGER NOT NULL"},
				{"compressedTokens", "INTEGER NOT NULL"},
				{"tokensSaved", "INTEGER NOT NULL"},
				{"durationMs", "INTEGER DEFAULT 0"},
				{"requestId", "TEXT"},
				{"actualPromptTokens", "INTEGER DEFAULT 0"},
				{"actualCompletionTokens", "INTEGER DEFAULT 0"},
				{"actualTotalTokens", "INTEGER DEFAULT 0"},
				{"actualCacheReadTokens", "INTEGER DEFAULT 0"},
				{"actualCacheWriteTokens", "INTEGER DEFAULT 0"},
				{"estimatedUsdSaved", "REAL DEFAULT 0"},
				{"skipReason", "TEXT"},
			},
			indexes: []string{
				"CREATE INDEX IF NOT EXISTS idx_ca_ts ON compressionAnalytics(timestamp DESC)",
				"CREATE INDEX IF NOT EXISTS idx_ca_provider ON compressionAnalytics(provider)",
				"CREATE INDEX IF NOT EXISTS idx_ca_model ON compressionAnalytics(model)",
				"CREATE INDEX IF NOT EXISTS idx_ca_mode ON compressionAnalytics(mode)",
				"CREATE INDEX IF NOT EXISTS idx_ca_req ON compressionAnalytics(requestId)",
				"CREATE INDEX IF NOT EXISTS idx_ca_ts_prov ON compressionAnalytics(timestamp DESC, provider)",
				"CREATE INDEX IF NOT EXISTS idx_ca_ts_model ON compressionAnalytics(timestamp DESC, model)",
			},
		},
		{
			name: "semanticCacheEntries",
			columns: [][2]string{
				{"key", "TEXT PRIMARY KEY"},
				{"model", "TEXT NOT NULL"},
				{"responseBody", "BLOB NOT NULL"},
				{"contentType", "TEXT"},
				{"storedAt", "TEXT NOT NULL"},
				{"hitCount", "INTEGER DEFAULT 0"},
				{"tokensSaved", "INTEGER DEFAULT 0"},
			},
			indexes: []string{
				"CREATE INDEX IF NOT EXISTS idx_sce_model ON semanticCacheEntries(model)",
				"CREATE INDEX IF NOT EXISTS idx_sce_stored ON semanticCacheEntries(storedAt)",
			},
		},
	}
}

// goOnlyColumns are additive columns the Go runtime writes that upstream
// schema.js does not declare. Added idempotently; upstream ignores them.
var goOnlyColumns = [][3]string{
	{"providerConnections", "lastUsedAt", "TEXT"},
	{"providerConnections", "consecutiveUseCount", "INTEGER DEFAULT 0"},
	// F-5 Credential Vault: sealed credential columns
	{"providerConnections", "secretWrappedDEK", "TEXT DEFAULT ''"},
	{"providerConnections", "secretCiphertext", "TEXT DEFAULT ''"},
	{"providerConnections", "tokenWrappedDEK", "TEXT DEFAULT ''"},
	{"providerConnections", "tokenCiphertext", "TEXT DEFAULT ''"},
	{"providerConnections", "refreshWrappedDEK", "TEXT DEFAULT ''"},
	{"providerConnections", "refreshCiphertext", "TEXT DEFAULT ''"},
	// F-1 Rate Limiting per-key
	{"apiKeys", "rateLimitRPM", "INTEGER DEFAULT 0"},
	{"apiKeys", "rateLimitTPM", "INTEGER DEFAULT 0"},
	{"apiKeys", "rateLimitConcurrency", "INTEGER DEFAULT 0"},
	// F-6 Argon2id key hashing
	{"apiKeys", "keyHash", "TEXT DEFAULT ''"},
	{"apiKeys", "lookupHash", "TEXT DEFAULT ''"},
	{"apiKeys", "keyDisplay", "TEXT DEFAULT ''"},
	// F-14 Time-limit keys + resale metadata
	{"apiKeys", "expiresAt", "TEXT DEFAULT ''"},
	{"apiKeys", "lastUsedAt", "TEXT DEFAULT ''"},
	{"apiKeys", "usedCount", "INTEGER DEFAULT 0"},
	{"apiKeys", "metadata", "TEXT DEFAULT ''"},
}

// keiRouterTables are Go-only tables for KeiRouter port features.
// All use IF NOT EXISTS and are additive-only.
var keiRouterTables = []tableDef{
	{
		name: "api_key_model_access",
		columns: [][2]string{
			{"api_key_id", "TEXT NOT NULL"},
			{"model", "TEXT NOT NULL"},
			{"created_at", "TEXT NOT NULL"},
		},
		primaryKey: "PRIMARY KEY (api_key_id, model)",
		indexes: []string{
			"CREATE INDEX IF NOT EXISTS idx_akma_key ON api_key_model_access(api_key_id)",
		},
	},
	{
		name: "guardrail_policies",
		columns: [][2]string{
			{"id", "TEXT PRIMARY KEY"},
			{"tenant_id", "TEXT NOT NULL DEFAULT 'default'"},
			{"scope", "TEXT NOT NULL"},
			{"scope_id", "TEXT NOT NULL DEFAULT ''"},
			{"name", "TEXT NOT NULL"},
			{"enabled", "INTEGER NOT NULL DEFAULT 1"},
			{"config", "TEXT NOT NULL DEFAULT '{}'"},
			{"created_at", "TEXT NOT NULL"},
			{"updated_at", "TEXT NOT NULL"},
		},
		indexes: []string{
			"CREATE UNIQUE INDEX IF NOT EXISTS idx_grp_scope ON guardrail_policies(tenant_id, scope, scope_id)",
		},
	},
	{
		name: "guardrail_logs",
		columns: [][2]string{
			{"id", "TEXT PRIMARY KEY"},
			{"tenant_id", "TEXT NOT NULL"},
			{"request_id", "TEXT DEFAULT ''"},
			{"api_key_id", "TEXT DEFAULT ''"},
			{"provider", "TEXT DEFAULT ''"},
			{"model", "TEXT DEFAULT ''"},
			{"chain_id", "TEXT DEFAULT ''"},
			{"detector", "TEXT NOT NULL"},
			{"direction", "TEXT NOT NULL"},
			{"action", "TEXT NOT NULL"},
			{"severity", "TEXT DEFAULT ''"},
			{"reason", "TEXT DEFAULT ''"},
			{"findings", "TEXT DEFAULT '[]'"},
			{"created_at", "TEXT NOT NULL"},
		},
		indexes: []string{
			"CREATE INDEX IF NOT EXISTS idx_grl_ts ON guardrail_logs(created_at DESC)",
		},
	},
}

// EnsureCoreSchema creates the upstream core tables/indexes when absent,
// backfills missing columns on existing databases, and seeds the minimal
// rows a fresh dashboard needs. Safe to call on every startup: every
// statement is IF NOT EXISTS / OR IGNORE / column-presence-checked, so
// existing user data is never touched.
func EnsureCoreSchema(db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("core schema: nil db")
	}
	for _, t := range coreSchema() {
		if err := ensureTable(db, t); err != nil {
			return err
		}
	}
	if err := EnsureKeiRouterTables(db); err != nil {
		return err
	}
	if err := EnsureAdditiveColumns(db); err != nil {
		return err
	}
	if _, err := db.Exec(
		`INSERT OR IGNORE INTO _meta(key, value) VALUES('schemaVersion', '1')`,
	); err != nil {
		return fmt.Errorf("core schema: seed _meta: %w", err)
	}
	if _, err := db.Exec(
		`INSERT OR IGNORE INTO settings(id, data) VALUES(1, '{}')`,
	); err != nil {
		return fmt.Errorf("core schema: seed settings: %w", err)
	}
	if err := BackfillCompressionAnalytics(db); err != nil {
		return err
	}
	return nil
}

// EnsureAdditiveColumns adds the Go-only columns to a database whose tables
// were created from the upstream core schema alone. Exported so test fixtures
// get the same shape as a real database instead of silently missing the
// round-robin bookkeeping columns or the KeiRouter per-key columns.
//
// A column whose table does not exist is skipped rather than treated as an
// error. In production EnsureCoreSchema has created every core table before
// this runs, so nothing is ever skipped; a test fixture that deliberately
// builds only the tables it exercises has no use for the other columns, and
// failing the whole pass over them would break fixtures testing unrelated
// behaviour.
func EnsureAdditiveColumns(db *sql.DB) error {
	for _, c := range goOnlyColumns {
		exists, err := tableExistsOnDB(db, c[0])
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		if err := addColumnIfMissing(db, c[0], c[1], c[2]); err != nil {
			return err
		}
	}
	return nil
}

// tableExistsOnDB reports whether a table is present, probed through sqlite_master
// so the answer does not depend on rewordable driver error strings.
func tableExistsOnDB(db *sql.DB, table string) (bool, error) {
	var count int
	err := db.QueryRow(
		`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?`, table,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("core schema: inspect table %s: %w", table, err)
	}
	return count > 0, nil
}


// EnsureKeiRouterTables creates the Go-only tables backing the KeiRouter port
// (per-key model access, guardrail policies, guardrail audit logs). Exported so
// a test fixture that only builds the upstream core tables still gets the full
// runtime shape instead of failing on a missing table at the first query.
func EnsureKeiRouterTables(db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("keirouter tables: nil db")
	}
	for _, t := range keiRouterTables {
		if err := ensureTable(db, t); err != nil {
			return err
		}
	}
	return nil
}

func ensureTable(db *sql.DB, t tableDef) error {
	defs := make([]string, 0, len(t.columns)+1)
	for _, c := range t.columns {
		defs = append(defs, c[0]+" "+c[1])
	}
	if t.primaryKey != "" {
		defs = append(defs, t.primaryKey)
	}
	if _, err := db.Exec(
		fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (%s)", t.name, strings.Join(defs, ", ")),
	); err != nil {
		return fmt.Errorf("core schema: create table %s: %w", t.name, err)
	}
	for _, c := range t.columns {
		if err := addColumnIfMissing(db, t.name, c[0], c[1]); err != nil {
			return err
		}
	}
	for _, idx := range t.indexes {
		if _, err := db.Exec(idx); err != nil {
			return fmt.Errorf("core schema: create index on %s: %w", t.name, err)
		}
	}
	return nil
}

// addColumnIfMissing ports the upstream sync guard: SQLite forbids PRIMARY
// KEY / UNIQUE inside ADD COLUMN, so those fragments are stripped — they
// only take effect at CREATE TABLE time on fresh databases.
func addColumnIfMissing(db *sql.DB, table, column, definition string) error {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return fmt.Errorf("core schema: inspect %s: %w", table, err)
	}
	defer rows.Close()
	var (
		cid, notNull, pk int
		name, colType    string
		dflt             sql.NullString
	)
	for rows.Next() {
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return fmt.Errorf("core schema: scan %s columns: %w", table, err)
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("core schema: read %s columns: %w", table, err)
	}
	safe := definition
	for _, frag := range []string{"PRIMARY KEY AUTOINCREMENT", "PRIMARY KEY", "UNIQUE"} {
		safe = strings.ReplaceAll(safe, frag, "")
	}
	safe = strings.TrimSpace(safe)
	safe = strings.Join(strings.Fields(safe), " ")
	if _, err := db.Exec(
		fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, safe),
	); err != nil {
		return fmt.Errorf("core schema: add column %s.%s: %w", table, column, err)
	}
	return nil
}
