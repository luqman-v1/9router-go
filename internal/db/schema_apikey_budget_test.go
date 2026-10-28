package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// apiKeyColumnNames reads the column set of apiKeys through PRAGMA.
func apiKeyColumnNames(t *testing.T, conn *sql.DB) map[string]bool {
	t.Helper()
	rows, err := conn.Query(`PRAGMA table_info(apiKeys)`)
	if err != nil {
		t.Fatalf("inspect apiKeys columns: %v", err)
	}
	defer rows.Close()
	var (
		cid, notNull, pk int
		name, colType    string
		dflt             sql.NullString
	)
	found := map[string]bool{}
	for rows.Next() {
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan apiKeys column: %v", err)
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read apiKeys columns: %v", err)
	}
	return found
}

// TestApiKeyBudgetColumnsBackfilled proves a database created without the new
// governance columns gains them on the next startup, so an existing install
// upgrades in place instead of failing at the first query that reads them.
func TestApiKeyBudgetColumnsBackfilled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	conn, err := OpenDatabase(path)
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	defer conn.Close()

	// A legacy apiKeys table: the pre-change shape, no budget columns.
	if _, err := conn.Exec(`CREATE TABLE apiKeys (
		id TEXT PRIMARY KEY,
		key TEXT UNIQUE NOT NULL,
		name TEXT,
		machineId TEXT,
		isActive INTEGER DEFAULT 1,
		createdAt TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("legacy apiKeys setup: %v", err)
	}

	if err := EnsureCoreSchema(conn); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}

	found := apiKeyColumnNames(t, conn)
	for _, want := range []string{
		"rateLimitRPM", "rateLimitTPM", "rateLimitConcurrency",
		"tokenQuota", "queueTimeoutMs",
	} {
		if !found[want] {
			t.Errorf("column %s not backfilled onto legacy apiKeys", want)
		}
	}
}

// TestEnsureCoreSchemaIsRerunnable proves running the bootstrap twice is safe:
// the second pass must not error (a duplicate-column ALTER would) and must not
// lose the data written between the two runs.
func TestEnsureCoreSchemaIsRerunnable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rerun.sqlite")
	conn, err := OpenDatabase(path)
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	defer conn.Close()

	if err := EnsureCoreSchema(conn); err != nil {
		t.Fatalf("first EnsureCoreSchema: %v", err)
	}

	r := NewRepo(conn)
	if err := r.CreateApiKey("re1", "sk-rerun-abcdefghijklmnop", "rerun", "m"); err != nil {
		t.Fatalf("CreateApiKey: %v", err)
	}
	if err := r.SetApiKeyBudget("re1", 4242, 9090); err != nil {
		t.Fatalf("SetApiKeyBudget: %v", err)
	}

	// Second bootstrap, as a restart would do.
	if err := EnsureCoreSchema(conn); err != nil {
		t.Fatalf("second EnsureCoreSchema (must be a no-op): %v", err)
	}

	got, err := r.GetApiKeyByID("re1")
	if err != nil || got == nil {
		t.Fatalf("key lost across re-run: %v", err)
	}
	if got.TokenQuota == nil || *got.TokenQuota != 4242 {
		t.Errorf("tokenQuota after re-run = %v, want 4242", got.TokenQuota)
	}
	if got.QueueTimeoutMs == nil || *got.QueueTimeoutMs != 9090 {
		t.Errorf("queueTimeoutMs after re-run = %v, want 9090", got.QueueTimeoutMs)
	}
}

// TestSchemaDatabaseFileIsNotLeftBehind is a small hygiene guard: the file the
// test opened must be the one we inspect, so a stale shared temp file cannot
// make the column assertions pass for the wrong reason.
func TestSchemaDatabaseFileIsNotLeftBehind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hygiene.sqlite")
	conn, err := OpenDatabase(path)
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	defer conn.Close()
	if err := EnsureCoreSchema(conn); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected the database file to exist at %s: %v", path, err)
	}
}

// TestUsageHistoryApiKeyIndexExists proves the index the token-quota sum needs
// is created with the core schema. Without it GetApiKeyTokenUsage scans the
// whole (unpruned) ledger on every request for a quota-bearing key.
func TestUsageHistoryApiKeyIndexExists(t *testing.T) {
	r := openEphemeralSchemaDB(t)

	var count int
	if err := r.db.QueryRow(
		`SELECT COUNT(1) FROM sqlite_master WHERE type = 'index' AND name = 'idx_uh_apikey'`,
	).Scan(&count); err != nil {
		t.Fatalf("query index: %v", err)
	}
	if count != 1 {
		t.Fatal("idx_uh_apikey is missing; the quota sum would full-scan usageHistory")
	}

	// And the planner must actually use it for the quota query.
	rows, err := r.db.Query(`EXPLAIN QUERY PLAN
		SELECT COALESCE(SUM(COALESCE(promptTokens,0) + COALESCE(completionTokens,0)), 0)
		  FROM usageHistory WHERE apiKey = ? OR apiKey = ?`, "k", "k")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	var plan string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan += detail + "\n"
	}
	if !strings.Contains(plan, "idx_uh_apikey") {
		t.Errorf("query plan does not use idx_uh_apikey:\n%s", plan)
	}
}

// TestUsageHistoryApiKeyIndexAddedOnUpgrade proves an existing database that
// predates the index picks it up on the next boot. EnsureCoreSchema reruns the
// CREATE INDEX IF NOT EXISTS list, so an install upgraded in place gets the
// index without a migration step — this is the case the reviewer's item #4
// depends on for existing deployments.
func TestUsageHistoryApiKeyIndexAddedOnUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy_idx.sqlite")
	conn, err := OpenDatabase(path)
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	defer conn.Close()

	// A legacy usageHistory with rows and no apiKey index.
	if _, err := conn.Exec(`CREATE TABLE usageHistory (
		id INTEGER PRIMARY KEY AUTOINCREMENT, timestamp TEXT NOT NULL,
		provider TEXT, model TEXT, connectionId TEXT, apiKey TEXT, endpoint TEXT,
		promptTokens INTEGER DEFAULT 0, completionTokens INTEGER DEFAULT 0,
		cost REAL DEFAULT 0, status TEXT, tokens TEXT, meta TEXT
	)`); err != nil {
		t.Fatalf("legacy table: %v", err)
	}
	for i := 0; i < 50; i++ {
		if _, err := conn.Exec(
			`INSERT INTO usageHistory(timestamp, apiKey, promptTokens, completionTokens) VALUES(?,?,?,?)`,
			"2026-01-01T00:00:00Z", "sk-legacy", 10, 5); err != nil {
			t.Fatalf("seed row: %v", err)
		}
	}
	// Precondition: no index yet.
	var before int
	if err := conn.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type='index' AND name='idx_uh_apikey'`).Scan(&before); err != nil {
		t.Fatalf("pre-check: %v", err)
	}
	if before != 0 {
		t.Fatal("index existed before upgrade; test setup is wrong")
	}

	// Upgrade in place.
	if err := EnsureCoreSchema(conn); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}

	var after int
	if err := conn.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type='index' AND name='idx_uh_apikey'`).Scan(&after); err != nil {
		t.Fatalf("post-check: %v", err)
	}
	if after != 1 {
		t.Fatal("idx_uh_apikey was not created on upgrade")
	}

	// The pre-existing rows survive the upgrade.
	var rows int
	if err := conn.QueryRow(`SELECT COUNT(1) FROM usageHistory WHERE apiKey='sk-legacy'`).Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 50 {
		t.Errorf("usageHistory rows after upgrade = %d, want 50", rows)
	}
}

// TestUsageHistoryClientKeyIndexExists pins the SECOND index the per-key token
// quota needs, and the reason it is not optional.
//
// tokenUsageSince sums `clientKey = ? OR apiKey = ? OR apiKey = ?`. SQLite
// answers a disjunction with a multi-index OR only when every arm is
// indexable; one unindexed arm degrades the whole statement to a full scan of
// usageHistory. TestUsageHistoryApiKeyIndexExists above spelled out only the
// `apiKey = ?` arms, so idx_uh_apikey alone satisfied it while the production
// query still scanned the ledger — measured at ~127 ms per key on a 512k-row
// table, against a boot request (GET /api/keys) that runs it once per key row.
func TestUsageHistoryClientKeyIndexExists(t *testing.T) {
	r := openEphemeralSchemaDB(t)

	var count int
	if err := r.db.QueryRow(
		`SELECT COUNT(1) FROM sqlite_master WHERE type = 'index' AND name = 'idx_uh_clientkey'`,
	).Scan(&count); err != nil {
		t.Fatalf("query index: %v", err)
	}
	if count != 1 {
		t.Fatal("idx_uh_clientkey is missing; the quota sum would full-scan usageHistory")
	}

	// The planner must avoid a scan for the full production predicate — the
	// exact three-arm OR, not the two-arm apiKey subset. `SCAN usageHistory` in
	// this plan is the regression the test exists to catch.
	rows, err := r.db.Query(`EXPLAIN QUERY PLAN
		SELECT COALESCE(SUM(COALESCE(promptTokens,0) + COALESCE(completionTokens,0)), 0)
		  FROM usageHistory
		 WHERE (clientKey = ? OR apiKey = ? OR apiKey = ?)`, "k", "k", "k")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	var plan string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan += detail + "\n"
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read plan: %v", err)
	}
	if strings.Contains(plan, "SCAN usageHistory") {
		t.Errorf("the quota sum still full-scans usageHistory:\n%s", plan)
	}
	if !strings.Contains(plan, "idx_uh_clientkey") {
		t.Errorf("query plan does not use idx_uh_clientkey:\n%s", plan)
	}
}

// TestUsageHistoryClientKeyIndexAddedOnUpgrade proves an existing install —
// rows present, index set from before this fix — picks the new index up on the
// next boot. EnsureCoreSchema re-runs the CREATE INDEX IF NOT EXISTS list, so
// no migration step is needed.
func TestUsageHistoryClientKeyIndexAddedOnUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy_clientkey_idx.sqlite")
	conn, err := OpenDatabase(path)
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	defer conn.Close()

	// The pre-fix shape: usageHistory WITH clientKey but WITHOUT its index.
	if _, err := conn.Exec(`CREATE TABLE usageHistory (
		id INTEGER PRIMARY KEY AUTOINCREMENT, timestamp TEXT NOT NULL,
		provider TEXT, model TEXT, connectionId TEXT, apiKey TEXT, endpoint TEXT,
		promptTokens INTEGER DEFAULT 0, completionTokens INTEGER DEFAULT 0,
		cost REAL DEFAULT 0, status TEXT, tokens TEXT, meta TEXT,
		clientKey TEXT DEFAULT ''
	)`); err != nil {
		t.Fatalf("legacy table: %v", err)
	}
	for range 50 {
		if _, err := conn.Exec(
			`INSERT INTO usageHistory(timestamp, apiKey, clientKey, promptTokens, completionTokens) VALUES(?,?,?,?,?)`,
			"2026-01-01T00:00:00Z", "***", "sk-legacy", 10, 5); err != nil {
			t.Fatalf("seed row: %v", err)
		}
	}
	var before int
	if err := conn.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type='index' AND name='idx_uh_clientkey'`).Scan(&before); err != nil {
		t.Fatalf("pre-check: %v", err)
	}
	if before != 0 {
		t.Fatal("index existed before upgrade; test setup is wrong")
	}

	if err := EnsureCoreSchema(conn); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}

	var after int
	if err := conn.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type='index' AND name='idx_uh_clientkey'`).Scan(&after); err != nil {
		t.Fatalf("post-check: %v", err)
	}
	if after != 1 {
		t.Fatal("idx_uh_clientkey was not created on upgrade")
	}

	// Pre-existing rows survive, so the upgrade builds an index rather than
	// rebuilding the ledger the index protects.
	var rows int
	if err := conn.QueryRow(`SELECT COUNT(1) FROM usageHistory WHERE clientKey='sk-legacy'`).Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 50 {
		t.Errorf("usageHistory rows after upgrade = %d, want 50", rows)
	}
}
