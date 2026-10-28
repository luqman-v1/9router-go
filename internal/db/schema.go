package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
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
				{"clientKey", "TEXT DEFAULT ''"},
			},
			indexes: []string{
				"CREATE INDEX IF NOT EXISTS idx_uh_ts ON usageHistory(timestamp DESC)",
				"CREATE INDEX IF NOT EXISTS idx_uh_model ON usageHistory(model)",
				"CREATE INDEX IF NOT EXISTS idx_uh_conn ON usageHistory(connectionId)",
				"CREATE INDEX IF NOT EXISTS idx_uh_ts_prov ON usageHistory(timestamp DESC, provider)",
				"CREATE INDEX IF NOT EXISTS idx_uh_ts_model ON usageHistory(timestamp DESC, model)",
				// The per-key lifetime token quota sums prompt+completion tokens
				// over `clientKey = ? OR apiKey = ? OR apiKey = ?` (tokenUsageSince,
				// quota_reset.go). SQLite answers a disjunction with a multi-index
				// OR only when EVERY arm is indexable, so idx_uh_apikey alone was
				// not enough: the unindexed clientKey arm degraded the whole
				// statement to `SCAN usageHistory`. That scan ran once per key row
				// in GET /api/keys (apiKeyUsedTokens), which is on the dashboard's
				// boot path — measured on a 512k-row ledger at ~127 ms per key, so
				// a 50-key install spent ~10 s in that one request. Both columns
				// need their own index for the OR to use indexes at all.
				"CREATE INDEX IF NOT EXISTS idx_uh_apikey ON usageHistory(apiKey)",
				"CREATE INDEX IF NOT EXISTS idx_uh_clientkey ON usageHistory(clientKey)",
				// A custom provider node's lifetime token quota is the sum of
				// prompt+completion over the rows whose provider is that node,
				// counted from the node's own reset mark. The upstream index on
				// (provider) alone served the equality but not the mark: the
				// sum still had to read the token columns off the table, one
				// row at a time, and the dashboard's node list pays that once
				// per node on every poll.
				//
				// Leading with the mark (id) after the provider lets SQLite
				// answer the same sum from the index alone: the rows a node
				// has spent are contiguous in the index, so the sum is a
				// covering-index range scan. Measured on a 400k-row ledger
				// with 200 custom nodes, the node list went from 1,505 ms to
				// 9 ms, and the per-node read the request path does went from
				// 0.5 ms to 0.1 ms.
				//
				// It replaces the single-column (provider) index rather than
				// sitting beside it: the composite answers the equality
				// search on its own, and SQLite would otherwise keep the
				// narrow index as a second candidate it has to maintain on
				// every insert. Measured over 50,000 inserts, keeping both
				// cost 71 us/row against 56 us/row for the composite alone.
				"CREATE INDEX IF NOT EXISTS idx_uh_prov_tok ON usageHistory(provider, id, promptTokens, completionTokens)",
				// The Usage page's two heaviest reads scan the trailing week of
				// the ledger: the Overview trend chart reads every row to split
				// it per model family, and the Details node-health table groups
				// it per provider and per model. Both read the row's wide
				// columns, and a usageHistory row carries the token payload in
				// JSON, so the scan had to fetch every row of the window:
				// measured at 6.1 s for the chart and 5.8 s + 6.4 s for the two
				// group reads on a 1.2M-row ledger, against 97 ms for the same
				// range counted through the index. This index carries every
				// column those three reads need, so SQLite answers them from
				// the index alone: 1.0 s, 1.1 s and 1.8 s on that ledger, with
				// no row of the window read.
				"CREATE INDEX IF NOT EXISTS idx_uh_analytics ON usageHistory(timestamp, provider, model, status, promptTokens, completionTokens, cost)",
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
				// The four indexes below exist for the analytics page, and the shape
				// of each one is forced by a measured plan rather than by what looks
				// natural. Against a 300k-run ledger the statements they serve fell
				// from 4944 ms to 129 ms combined; every one of them was previously
				// reading the table once per matching row.
				//
				// The breakdowns lead with the group key rather than with timestamp.
				// A timestamp-led index can only serve a range predicate by seeking,
				// and since timestamp is not the rowid it then has to return to the
				// table for every column the SELECT reads — the index removes the
				// scan and adds a lookup per row. Leading with the group key instead
				// puts the timestamp inside the key, so the window filter still
				// applies, the groups come out already clustered, and carrying the
				// read columns makes the pass index-only: 1659 ms -> 50 ms for the
				// model breakdown, 351 ms -> 37 ms for the provider one.
				//
				// idx_ca_ts_prov and idx_ca_ts_model, which led with timestamp, are
				// gone: SQLite never chose them over the single-column index it was
				// reaching for instead, so they only cost every insert.
				"CREATE INDEX IF NOT EXISTS idx_ca_stat_p ON compressionAnalytics(provider, timestamp, tokensSaved, originalTokens, durationMs)",
				"CREATE INDEX IF NOT EXISTS idx_ca_stat_m ON compressionAnalytics(model, timestamp, tokensSaved, originalTokens, durationMs)",
				// Top 10 savers orders the window by tokensSaved, which nothing
				// carried, so SQLite sorted every matching row to return ten. The
				// sort column leads because SQLite has to walk the index to answer the
				// ORDER BY either way: 971 ms -> under a millisecond, returning the
				// same ten rows.
				"CREATE INDEX IF NOT EXISTS idx_ca_saved ON compressionAnalytics(tokensSaved, timestamp)",
				// The receipts aggregate filters on the actual-token columns and
				// reads neither of them, so the timestamp index sent it to the table
				// once per row: 940 ms -> 17 ms.
				"CREATE INDEX IF NOT EXISTS idx_ca_receipt ON compressionAnalytics(timestamp, actualPromptTokens, actualTotalTokens)",
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
		{
			name: "files",
			columns: [][2]string{
				{"id", "TEXT PRIMARY KEY"},
				{"filename", "TEXT NOT NULL"},
				{"bytes", "INTEGER NOT NULL"},
				{"purpose", "TEXT NOT NULL"},
				{"created_at", "INTEGER NOT NULL"},
				{"content", "BLOB NOT NULL"},
			},
			indexes: []string{
				"CREATE INDEX IF NOT EXISTS idx_files_purpose ON files(purpose)",
				"CREATE INDEX IF NOT EXISTS idx_files_created ON files(created_at DESC)",
			},
		},
		{
			name: "batches",
			columns: [][2]string{
				{"id", "TEXT PRIMARY KEY"},
				{"input_file_id", "TEXT NOT NULL"},
				{"endpoint", "TEXT NOT NULL"},
				{"completion_window", "TEXT NOT NULL"},
				{"status", "TEXT NOT NULL"},
				{"output_file_id", "TEXT"},
				{"error_file_id", "TEXT"},
				{"created_at", "INTEGER NOT NULL"},
				{"completed_at", "INTEGER"},
				{"cancelled_at", "INTEGER"},
				{"metadata", "TEXT"},
			},
			indexes: []string{
				"CREATE INDEX IF NOT EXISTS idx_batches_status ON batches(status)",
				"CREATE INDEX IF NOT EXISTS idx_batches_created ON batches(created_at DESC)",
				"CREATE INDEX IF NOT EXISTS idx_batches_input_file ON batches(input_file_id)",
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
	// F-1b Budget & queue: cumulative token quota and how long a request may
	// wait for a concurrency slot before it is refused.
	{"apiKeys", "tokenQuota", "INTEGER DEFAULT 0"},
	{"apiKeys", "queueTimeoutMs", "INTEGER DEFAULT 0"},
	// F-6 Argon2id key hashing
	{"apiKeys", "keyHash", "TEXT DEFAULT ''"},
	{"apiKeys", "lookupHash", "TEXT DEFAULT ''"},
	{"apiKeys", "keyDisplay", "TEXT DEFAULT ''"},
	// F-14 Time-limit keys + resale metadata
	{"apiKeys", "expiresAt", "TEXT DEFAULT ''"},
	{"apiKeys", "lastUsedAt", "TEXT DEFAULT ''"},
	{"apiKeys", "usedCount", "INTEGER DEFAULT 0"},
	{"apiKeys", "metadata", "TEXT DEFAULT ''"},
	// #365 Resettable token budget: the usageHistory row id a key's tokenQuota is
	// measured from, so an exhausted key can be given a fresh budget without
	// deleting the usage history behind it. A row id rather than a timestamp
	// because usageHistory.timestamp is second-precision and a reset in the same
	// second as the spend would keep charging the key.
	{"apiKeys", "quotaResetId", "INTEGER DEFAULT 0"},
	// #365 Client attribution in the usage ledger. usageHistory.apiKey holds the
	// UPSTREAM credential (masked), so a per-key token budget measured from that
	// column never matched a client and never fired. This column names the client
	// that spent the tokens: the same masked shape the reader already looks for,
	// so rows the upstream dashboard wrote (client key in apiKey) keep counting
	// through the fallback term.
	{"usageHistory", "clientKey", "TEXT DEFAULT ''"},
	// F-3 Plans: reusable policy template
	{"apiKeys", "planId", "TEXT DEFAULT ''"},
	// Per-custom-provider capacity limits (docs/provider-limits-plan.md). These
	// bound what one upstream endpoint tolerates, which is a different axis from
	// the apiKeys limits above (what one caller may spend): a custom provider is
	// one providerNodes row whatever it calls, so the policy hangs off the node
	// every one of its connections shares. 0 means unlimited and "" means no
	// expiry, the same convention rateLimitRPM and expiresAt already use.
	{"providerNodes", "rpmLimit", "INTEGER DEFAULT 0"},
	{"providerNodes", "tpmLimit", "INTEGER DEFAULT 0"},
	{"providerNodes", "concurrencyLimit", "INTEGER DEFAULT 0"},
	{"providerNodes", "tokenQuota", "INTEGER DEFAULT 0"},
	{"providerNodes", "queueTimeoutMs", "INTEGER DEFAULT 0"},
	{"providerNodes", "quotaResetId", "INTEGER DEFAULT 0"},
	{"providerNodes", "contractExpiresAt", "TEXT DEFAULT ''"},
	// Smart select combo: the pattern rules that keep a combo's membership in
	// step with the models the gateway knows about. A JSON array of rule
	// objects; "[]" means no rules, which is what every combo written before
	// the feature holds. It is bookkeeping the resolver never reads — the
	// models column stays the wire contract — so upstream, which declares
	// neither the column nor the concept, ignores it.
	{"combos", "matchRules", "TEXT DEFAULT '[]'"},
}

// keiRouterTables are Go-only tables for KeiRouter port features.
var keiRouterTables = []tableDef{
	{
		name: "plans",
		columns: [][2]string{
			{"id", "TEXT PRIMARY KEY"},
			{"tenant_id", "TEXT NOT NULL DEFAULT 'default'"},
			{"name", "TEXT NOT NULL"},
			{"description", "TEXT DEFAULT ''"},
			{"limit_micros", "INTEGER NOT NULL DEFAULT 0"},
			{"limit_tokens", "INTEGER NOT NULL DEFAULT 0"},
			{"rpm_limit", "INTEGER NOT NULL DEFAULT 0"},
			{"tpm_limit", "INTEGER NOT NULL DEFAULT 0"},
			{"concurrency_limit", "INTEGER NOT NULL DEFAULT 0"},
			{"period", "TEXT NOT NULL DEFAULT 'monthly'"},
			{"alert_pct", "INTEGER NOT NULL DEFAULT 80"},
			{"hard_cutoff", "INTEGER NOT NULL DEFAULT 1"},
			{"allowed_models", "TEXT DEFAULT ''"},
			{"created_at", "TEXT NOT NULL"},
			{"updated_at", "TEXT NOT NULL"},
		},
		indexes: []string{
			"CREATE INDEX IF NOT EXISTS idx_plans_tenant ON plans(tenant_id)",
		},
	},
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

// usageLedgerTables are Go-only tables backing the ledger retention in
// usage_retention_ledger.go. They are declared apart from keiRouterTables
// because they are not a ported feature: they exist so an opt-in prune of the
// billing ledger cannot refund a per-key token budget.
//
// usageLedgerCarryover is one row per client key and is read by exactly one
// statement (tokenUsageSince). It is deliberately NOT a synthetic row inside
// usageHistory: a marker row there would be indistinguishable from real traffic
// for every other ledger consumer — the sub-day usage window, the daily chart's
// per-family split, the analytics scans, the topology map — each of which would
// then need its own filter, and any one of them forgetting it reports spend that
// never happened.
//
// providerLedgerCarryover is the same mechanism for a custom provider's token
// quota: usageHistory.provider holds the node id for every request routed
// through it (resolution.go returns node.ID), so a node's spend is one indexed
// equality — and therefore one more budget a prune could silently refund if the
// tokens it deletes were not carried somewhere. One row per node, read by
// ProviderTokenUsage.
var usageLedgerTables = []tableDef{
	{
		name: "usageLedgerCarryover",
		columns: [][2]string{
			{"keyId", "TEXT PRIMARY KEY"},
			{"tokens", "INTEGER NOT NULL DEFAULT 0"},
			{"updatedAt", "TEXT NOT NULL"},
		},
	},
	{
		name: "providerLedgerCarryover",
		columns: [][2]string{
			{"providerId", "TEXT PRIMARY KEY"},
			{"tokens", "INTEGER NOT NULL DEFAULT 0"},
			{"updatedAt", "TEXT NOT NULL"},
		},
	},
}

// EnsureLedgerTables creates the Go-only tables the ledger retention needs.
// Exported for the same reason as EnsureKeiRouterTables: a test fixture that
// builds only the upstream core schema still gets the shape the runtime queries.
func EnsureLedgerTables(db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("usage ledger tables: nil db")
	}
	for _, t := range usageLedgerTables {
		if err := ensureTable(db, t); err != nil {
			return err
		}
	}
	return nil
}

// supersededIndexes are dropped on every boot: each was replaced by an index
// whose shape a measured query plan chose, and keeping the old ones would leave
// SQLite with candidates it never picks while every write still updates them.
//
// idx_ca_ts_prov and idx_ca_ts_model are timestamp-led composites built for the
// analytics breakdowns before those breakdowns carried their group key first.
// Against a 300k-run ledger neither appeared in a single plan — SQLite reached
// for the single-column index instead — while the breakdown they were named
// for was still reading the table once per matching row.
//
// idx_uh_provider is the single-column (provider) index, replaced by
// idx_uh_prov_tok. The composite answers the same equality search and
// carries the token columns with it, so the narrow one is a candidate SQLite
// no longer picks and a second index every ledger insert has to maintain.
var supersededIndexes = []string{
	"DROP INDEX IF EXISTS idx_ca_ts_prov",
	"DROP INDEX IF EXISTS idx_ca_ts_model",
	"DROP INDEX IF EXISTS idx_uh_provider",
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

	// Indexes that a measured plan replaced. This is the one destructive
	// statement in the bootstrap, and it is here because leaving them costs
	// every insert to maintain an index SQLite never reads: against a 300k-run
	// ledger the timestamp-led composites were not chosen for any analytics
	// statement, while each of them still had to be updated on every write.
	//
	// It drops only indexes this build created and no query references any
	// more. A user's rows, columns and every other index are untouched — this
	// is a schema object, not data.
	for _, superseded := range supersededIndexes {
		if _, err := db.Exec(superseded); err != nil {
			return fmt.Errorf("core schema: drop superseded index: %w", err)
		}
	}
	if err := EnsureKeiRouterTables(db); err != nil {
		return err
	}
	if err := EnsureLedgerTables(db); err != nil {
		return err
	}
	if err := EnsureThoughtSignatureTables(db); err != nil {
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
	// The default plan is part of the table's shape, not operator data: every
	// path that creates the plans table also guarantees the row exists, so a
	// key assigned to "default" always resolves to a plan instead of silently
	// reading as unplanned. INSERT OR IGNORE keeps it stable across boots.
	if _, err := db.Exec( //nolint:noctx
		`INSERT OR IGNORE INTO plans (id, tenant_id, name, description, limit_micros, limit_tokens, rpm_limit, tpm_limit, concurrency_limit, period, alert_pct, hard_cutoff, allowed_models, created_at, updated_at)
		 VALUES ('default', 'default', 'Default', 'Default plan for existing keys', 0, 0, 0, 0, 0, 'monthly', 80, 0, '', ?, ?)`,
		time.Now().UTC().Format(time.RFC3339),
		time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		return fmt.Errorf("keirouter tables: seed default plan: %w", err)
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
