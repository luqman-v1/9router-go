package db

import (
	"strings"
	"testing"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/models"
)

// TestGetApiKeyTokenUsageForKeysMatchesPerKeyRead proves the batched page read
// returns exactly what the per-key reader returns for the same rows: every
// recorded spelling summed once, the reset mark honoured, the carryover added,
// and unrelated keys' rows left out.
func TestGetApiKeyTokenUsageForKeysMatchesPerKeyRead(t *testing.T) {
	r := openEphemeralSchemaDB(t)

	const (
		rawSum    = "sk-batch-sum-key-000000000001"
		rawReset  = "sk-batch-reset-key-00000000002"
		rawCarry  = "sk-batch-carry-key-00000000003"
		rawIdle   = "sk-batch-idle-key-000000000004"
		rawUnseen = "sk-batch-unseen-key-000000005"
	)
	for id, raw := range map[string]string{"ks": rawSum, "kr": rawReset, "kc": rawCarry, "ki": rawIdle, "ku": rawUnseen} {
		if err := r.CreateApiKey(id, raw, id, ""); err != nil {
			t.Fatalf("CreateApiKey(%s): %v", id, err)
		}
	}

	// The three spellings a key's spend can be recorded under, plus a row
	// belonging to a key outside the page.
	insert := func(rawKey string, prompt, completion int) {
		t.Helper()
		if err := r.InsertUsageHistory("openai", "gpt-4o", "c1", rawKey,
			"/v1/chat/completions", prompt, completion, 0, "success", prompt+completion, "{}", "{}"); err != nil {
			t.Fatalf("InsertUsageHistory(%s): %v", rawKey, err)
		}
	}
	insert(handlerutil.MaskAPIKey(rawSum), 100, 50)
	insert(rawSum, 200, 25)
	insert(handlerutil.MaskAPIKey(rawSum), 10, 5)
	insert(rawUnseen, 999, 999)

	// A reset part-way through, with spend on both sides of the mark.
	insert(rawReset, 400, 100)
	mark, err := r.ResetApiKeyQuota(t.Context(), "kr")
	if err != nil {
		t.Fatalf("ResetApiKeyQuota: %v", err)
	}
	if mark <= 0 {
		t.Fatalf("reset mark = %d, want a positive usageHistory row id", mark)
	}
	insert(rawReset, 7, 3)

	// Carryover stands for rows a ledger prune already deleted.
	if _, err := r.db.Exec(`INSERT INTO usageLedgerCarryover (keyId, tokens, updatedAt) VALUES ('kc', 1234, '2026-10-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed carryover: %v", err)
	}

	keys, err := r.GetApiKeys()
	if err != nil {
		t.Fatalf("GetApiKeys: %v", err)
	}

	batched, err := r.GetApiKeyTokenUsageForKeys(t.Context(), keys)
	if err != nil {
		t.Fatalf("GetApiKeyTokenUsageForKeys: %v", err)
	}

	for _, k := range keys {
		perKey, err := r.GetApiKeyTokenUsageByID(t.Context(), k.ID)
		if err != nil {
			t.Fatalf("GetApiKeyTokenUsageByID(%s): %v", k.ID, err)
		}
		if got := batched[k.ID]; got != perKey {
			t.Errorf("key %s: batched %d != per-key %d", k.ID, got, perKey)
		}
	}

	if got := batched["ks"]; got != 100+50+200+25+10+5 {
		t.Errorf("sum key = %d, want %d", got, 100+50+200+25+10+5)
	}
	if got := batched["kr"]; got != 10 {
		t.Errorf("reset key = %d, want 10 (only post-mark spend)", got)
	}
	if got := batched["kc"]; got != 1234 {
		t.Errorf("carryover key = %d, want 1234", got)
	}
	if got := batched["ki"]; got != 0 {
		t.Errorf("idle key = %d, want 0", got)
	}
	if got := batched["ku"]; got != 1998 {
		t.Errorf("owning key = %d, want 1998: its rows must not leak into any other key's sum", got)
	}
}

// TestGetApiKeyTokenUsageForKeysQueryPlan proves the batched statement is
// answered through the two ledger indexes rather than a scan. A scan here is
// the regression the idx_uh_clientkey_tok/idx_uh_apikey_tok pair exists to prevent,
// and it costs one full pass per key.
func TestGetApiKeyTokenUsageForKeysQueryPlan(t *testing.T) {
	r := openEphemeralSchemaDB(t)

	if err := r.CreateApiKey("kp", "sk-plan-key-0000000000000001", "plan key", ""); err != nil {
		t.Fatalf("CreateApiKey: %v", err)
	}

	rows, err := r.db.Query(`EXPLAIN QUERY PLAN `+apiKeyTokenUsageBatchQuery(1),
		"kp", "sk-plan-key-0000000000000001", handlerutil.MaskAPIKey("sk-plan-key-0000000000000001"), 0)
	if err != nil {
		t.Fatalf("explain query plan: %v", err)
	}
	defer rows.Close()

	var plans []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plans = append(plans, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate plan: %v", err)
	}

	joined := strings.Join(plans, "\n")
	if strings.Contains(joined, "SCAN usageHistory") {
		t.Errorf("batched read scans usageHistory instead of using the indexes:\n%s", joined)
	}
	if !strings.Contains(joined, "idx_uh_apikey_tok") || !strings.Contains(joined, "idx_uh_clientkey_tok") {
		t.Errorf("batched read does not use both ledger indexes:\n%s\nplan:\n%s",
			apiKeyTokenUsageBatchQuery(1), joined)
	}
}

// TestGetApiKeyTokenUsageForKeysLegacySchema proves a pre-#365 database (no
// quotaResetId column) still gets correct numbers: the batched statement
// cannot compile there, so the reader falls back to the per-key path that
// already degrades the missing column to a lifetime sum.
func TestGetApiKeyTokenUsageForKeysLegacySchema(t *testing.T) {
	r := openLegacyQuotaDB(t)

	if _, err := r.db.Exec(`INSERT INTO apiKeys (id, key, name, isActive, createdAt) VALUES ('kl', 'sk-legacy-batch-key-00000001', 'legacy', 1, '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed legacy key: %v", err)
	}
	if _, err := r.db.Exec(`INSERT INTO usageHistory (provider, model, connectionId, apiKey, endpoint, promptTokens, completionTokens, status, timestamp) VALUES ('openai','gpt-4o','c1','sk-legacy-batch-key-00000001','/v1/chat/completions',10,5,'success','2026-01-01T00:00:01Z')`); err != nil {
		t.Fatalf("seed legacy usage: %v", err)
	}

	keys, err := r.GetApiKeys()
	if err != nil {
		t.Fatalf("GetApiKeys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("GetApiKeys returned %d keys, want 1", len(keys))
	}

	batched, err := r.GetApiKeyTokenUsageForKeys(t.Context(), keys)
	if err != nil {
		t.Fatalf("GetApiKeyTokenUsageForKeys on legacy schema: %v", err)
	}
	if got := batched[keys[0].ID]; got != 15 {
		t.Errorf("legacy schema spend = %d, want 15", got)
	}
}

func TestGetApiKeyTokenUsageForKeysEmptyAndNil(t *testing.T) {
	r := openEphemeralSchemaDB(t)

	if usage, err := r.GetApiKeyTokenUsageForKeys(t.Context(), nil); err != nil || len(usage) != 0 {
		t.Errorf("nil page = (%v, %v), want (empty, nil)", usage, err)
	}

	usage, err := r.GetApiKeyTokenUsageForKeys(t.Context(), []*models.APIKey{nil, {ID: "", Key: "sk-x"}})
	if err != nil {
		t.Fatalf("degenerate page: %v", err)
	}
	if len(usage) != 0 {
		t.Errorf("degenerate page usage = %v, want empty", usage)
	}
}

// openLegacyQuotaDB builds the apiKeys/usageHistory pair as it existed before
// the resettable token budget (#365): every column the shared key reader
// selects, but no quotaResetId, and no EnsureCoreSchema pass to add it.
func openLegacyQuotaDB(t *testing.T) *Repo {
	t.Helper()
	conn, err := OpenDatabase(t.TempDir() + "/legacy.sqlite")
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	if _, err := conn.Exec(`CREATE TABLE apiKeys (
		id TEXT PRIMARY KEY,
		key TEXT UNIQUE NOT NULL,
		name TEXT,
		machineId TEXT,
		isActive INTEGER DEFAULT 1,
		createdAt TEXT NOT NULL,
		rateLimitRPM INTEGER DEFAULT 0,
		rateLimitTPM INTEGER DEFAULT 0,
		rateLimitConcurrency INTEGER DEFAULT 0,
		tokenQuota INTEGER DEFAULT 0,
		queueTimeoutMs INTEGER DEFAULT 0,
		keyHash TEXT DEFAULT '',
		lookupHash TEXT DEFAULT '',
		keyDisplay TEXT DEFAULT '',
		expiresAt TEXT DEFAULT '',
		lastUsedAt TEXT DEFAULT '',
		usedCount INTEGER DEFAULT 0,
		metadata TEXT DEFAULT '',
		planId TEXT DEFAULT ''
	)`); err != nil {
		t.Fatalf("legacy apiKeys: %v", err)
	}
	if _, err := conn.Exec(`CREATE TABLE usageHistory (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		provider TEXT,
		model TEXT,
		connectionId TEXT,
		apiKey TEXT,
		endpoint TEXT,
		promptTokens INTEGER DEFAULT 0,
		completionTokens INTEGER DEFAULT 0,
		cost REAL DEFAULT 0,
		status TEXT,
		tokens TEXT,
		meta TEXT,
		clientKey TEXT DEFAULT '',
		timestamp TEXT
	)`); err != nil {
		t.Fatalf("legacy usageHistory: %v", err)
	}
	// The carryover table predates nothing: the per-key reader joins it
	// unconditionally, so the legacy shape here is the one from just before
	// quotaResetId, with the carryover table already present.
	if _, err := conn.Exec(`CREATE TABLE usageLedgerCarryover (
		keyId TEXT PRIMARY KEY,
		tokens INTEGER NOT NULL DEFAULT 0,
		updatedAt TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("legacy carryover: %v", err)
	}
	return NewRepo(conn)
}
