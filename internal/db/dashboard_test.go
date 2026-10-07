package db

import (
	"os"
	"testing"
	"time"

	"github.com/samber/lo"
)

func setupDashboardTestDB(t *testing.T) (*Repo, func()) {
	t.Helper()
	tmpFile, err := os.CreateTemp("", "test_dashboard_*.sqlite")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	tmpFile.Close()

	db, err := OpenDatabase(tmpFile.Name())
	if err != nil {
		os.Remove(tmpFile.Name())
		t.Fatalf("OpenDatabase failed: %v", err)
	}

	cleanup := func() {
		db.Close()
		os.Remove(tmpFile.Name())
	}

	schema := []string{
		`CREATE TABLE apiKeys (
			id TEXT PRIMARY KEY,
			key TEXT UNIQUE NOT NULL,
			name TEXT,
			machineId TEXT,
			isActive INTEGER DEFAULT 1,
			createdAt TEXT NOT NULL
		);`,
		`CREATE TABLE providerConnections (
			id TEXT PRIMARY KEY,
			provider TEXT NOT NULL,
			authType TEXT NOT NULL,
			name TEXT,
			email TEXT,
			priority INTEGER,
			isActive INTEGER DEFAULT 1,
			data TEXT NOT NULL,
			lastUsedAt TEXT,
			consecutiveUseCount INTEGER DEFAULT 0,
			createdAt TEXT NOT NULL,
			updatedAt TEXT NOT NULL
		);`,
		`CREATE TABLE combos (
			id TEXT PRIMARY KEY,
			name TEXT UNIQUE NOT NULL,
			kind TEXT,
			models TEXT NOT NULL,
			createdAt TEXT NOT NULL,
			updatedAt TEXT NOT NULL
		);`,
		`CREATE TABLE kv (
			scope TEXT NOT NULL,
			key TEXT NOT NULL,
			value TEXT NOT NULL,
			PRIMARY KEY (scope, key)
		);`,
		`CREATE TABLE settings (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			data TEXT NOT NULL
		);`,
	}

	for _, query := range schema {
		if _, err := db.Exec(query); err != nil {
			cleanup()
			t.Fatalf("failed to create table: %v", err)
		}
	}

	// Production adds the Go-only columns on every startup; a fixture that
	// skips this fails on any query that reads them.
	if err := EnsureAdditiveColumns(db); err != nil {
		cleanup()
		t.Fatalf("failed to apply additive columns: %v", err)
	}

	return NewRepo(db), cleanup
}

func TestProviderConnectionsCRUD(t *testing.T) {
	repo, cleanup := setupDashboardTestDB(t)
	defer cleanup()

	// Seed connection
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := repo.db.Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?)`,
		"conn-1", "openai", "apikey", "OpenAI Prod", 1, `{"apiKey":"sk-1"}`, now, now,
	)
	if err != nil {
		t.Fatalf("failed to seed provider connection: %v", err)
	}

	// Test UpdateProviderConnection
	err = repo.UpdateProviderConnection("conn-1", "OpenAI Updated", lo.ToPtr(5), false, `{"apiKey":"sk-2"}`)
	if err != nil {
		t.Fatalf("UpdateProviderConnection failed: %v", err)
	}

	var name, data string
	var priority, isActive int
	err = repo.db.QueryRow(
		`SELECT name, priority, isActive, data FROM providerConnections WHERE id = ?`, "conn-1",
	).Scan(&name, &priority, &isActive, &data)
	if err != nil {
		t.Fatalf("failed to query updated connection: %v", err)
	}
	if name != "OpenAI Updated" || priority != 5 || isActive != 0 || data != `{"apiKey":"sk-2"}` {
		t.Errorf("unexpected updated values: name=%s, priority=%d, isActive=%d, data=%s", name, priority, isActive, data)
	}

	// Test SetConnectionStatus
	err = repo.SetConnectionStatus("conn-1", true)
	if err != nil {
		t.Fatalf("SetConnectionStatus(true) failed: %v", err)
	}
	err = repo.db.QueryRow(`SELECT isActive FROM providerConnections WHERE id = ?`, "conn-1").Scan(&isActive)
	if err != nil || isActive != 1 {
		t.Errorf("expected isActive=1, got %d (err: %v)", isActive, err)
	}

	err = repo.SetConnectionStatus("conn-1", false)
	if err != nil {
		t.Fatalf("SetConnectionStatus(false) failed: %v", err)
	}
	err = repo.db.QueryRow(`SELECT isActive FROM providerConnections WHERE id = ?`, "conn-1").Scan(&isActive)
	if err != nil || isActive != 0 {
		t.Errorf("expected isActive=0, got %d (err: %v)", isActive, err)
	}

	// Test SetConnectionPriority
	err = repo.SetConnectionPriority("conn-1", 42)
	if err != nil {
		t.Fatalf("SetConnectionPriority failed: %v", err)
	}
	err = repo.db.QueryRow(`SELECT priority FROM providerConnections WHERE id = ?`, "conn-1").Scan(&priority)
	if err != nil || priority != 42 {
		t.Errorf("expected priority=42, got %d (err: %v)", priority, err)
	}

	// Test DeleteProviderConnection
	err = repo.DeleteProviderConnection("conn-1")
	if err != nil {
		t.Fatalf("DeleteProviderConnection failed: %v", err)
	}
	var count int
	err = repo.db.QueryRow(`SELECT count(*) FROM providerConnections WHERE id = ?`, "conn-1").Scan(&count)
	if err != nil || count != 0 {
		t.Errorf("expected connection to be deleted, count=%d (err: %v)", count, err)
	}
}

func TestCombosCRUD(t *testing.T) {
	repo, cleanup := setupDashboardTestDB(t)
	defer cleanup()

	// Test CreateCombo
	err := repo.CreateCombo("combo-1", "fast-models", "chat", `["openai/gpt-4o-mini"]`, "fallback")
	if err != nil {
		t.Fatalf("CreateCombo failed: %v", err)
	}

	var name, kind, models string
	err = repo.db.QueryRow(`SELECT name, kind, models FROM combos WHERE id = ?`, "combo-1").Scan(&name, &kind, &models)
	if err != nil {
		t.Fatalf("failed to query created combo: %v", err)
	}
	if name != "fast-models" || kind != "chat" || models != `["openai/gpt-4o-mini"]` {
		t.Errorf("unexpected combo values: name=%s, kind=%s, models=%s", name, kind, models)
	}

	// Test UpdateCombo
	err = repo.UpdateCombo("combo-1", "fast-models-v2", "chat", `["openai/gpt-4o","anthropic/claude-3-5-sonnet"]`, "round-robin")
	if err != nil {
		t.Fatalf("UpdateCombo failed: %v", err)
	}

	err = repo.db.QueryRow(`SELECT name, kind, models FROM combos WHERE id = ?`, "combo-1").Scan(&name, &kind, &models)
	if err != nil {
		t.Fatalf("failed to query updated combo: %v", err)
	}
	if name != "fast-models-v2" || models != `["openai/gpt-4o","anthropic/claude-3-5-sonnet"]` {
		t.Errorf("unexpected updated combo: name=%s, models=%s", name, models)
	}

	// Test DeleteCombo
	err = repo.DeleteCombo("combo-1")
	if err != nil {
		t.Fatalf("DeleteCombo failed: %v", err)
	}
	var count int
	err = repo.db.QueryRow(`SELECT count(*) FROM combos WHERE id = ?`, "combo-1").Scan(&count)
	if err != nil || count != 0 {
		t.Errorf("expected combo to be deleted, count=%d (err: %v)", count, err)
	}
}

func TestApiKeysCRUD(t *testing.T) {
	repo, cleanup := setupDashboardTestDB(t)
	defer cleanup()

	// Test GetApiKeys when empty
	keys, err := repo.GetApiKeys()
	if err != nil {
		t.Fatalf("GetApiKeys on empty table failed: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("expected 0 keys, got %d", len(keys))
	}

	// Test CreateApiKey
	err = repo.CreateApiKey("key-1", "sk-key-1", "Key One", "machine-a")
	if err != nil {
		t.Fatalf("CreateApiKey failed: %v", err)
	}
	_, err = repo.db.Exec(`UPDATE apiKeys SET createdAt = ? WHERE id = ?`, time.Now().UTC().Add(-1*time.Hour).Format(time.RFC3339), "key-1")
	if err != nil {
		t.Fatalf("failed to update key-1 createdAt: %v", err)
	}

	err = repo.CreateApiKey("key-2", "sk-key-2", "Key Two", "")
	if err != nil {
		t.Fatalf("CreateApiKey 2 failed: %v", err)
	}

	// Test GetApiKeys ordering (createdAt DESC)
	keys, err = repo.GetApiKeys()
	if err != nil {
		t.Fatalf("GetApiKeys failed: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
	if keys[0].ID != "key-2" || keys[1].ID != "key-1" {
		t.Errorf("expected keys ordered by createdAt DESC: [0].ID=%s, [1].ID=%s", keys[0].ID, keys[1].ID)
	}
	if keys[0].IsActive != 1 || keys[1].IsActive != 1 {
		t.Errorf("expected created keys to have isActive=1")
	}

	// Test SetApiKeyStatus
	err = repo.SetApiKeyStatus("key-1", false)
	if err != nil {
		t.Fatalf("SetApiKeyStatus(false) failed: %v", err)
	}
	var isActive int
	err = repo.db.QueryRow(`SELECT isActive FROM apiKeys WHERE id = ?`, "key-1").Scan(&isActive)
	if err != nil || isActive != 0 {
		t.Errorf("expected isActive=0, got %d (err: %v)", isActive, err)
	}

	// Test DeleteApiKey
	err = repo.DeleteApiKey("key-1")
	if err != nil {
		t.Fatalf("DeleteApiKey failed: %v", err)
	}
	keys, err = repo.GetApiKeys()
	if err != nil {
		t.Fatalf("GetApiKeys after delete failed: %v", err)
	}
	if len(keys) != 1 || keys[0].ID != "key-2" {
		t.Errorf("expected 1 key (key-2), got %d", len(keys))
	}
}

func TestKVCRUD(t *testing.T) {
	repo, cleanup := setupDashboardTestDB(t)
	defer cleanup()

	// Test SetKV
	err := repo.SetKV("disabledModels", "openai", `["gpt-3.5-turbo"]`)
	if err != nil {
		t.Fatalf("SetKV failed: %v", err)
	}
	err = repo.SetKV("disabledModels", "anthropic", `["claude-2"]`)
	if err != nil {
		t.Fatalf("SetKV 2 failed: %v", err)
	}
	err = repo.SetKV("customModels", "gemini", `{"id":"custom-1"}`)
	if err != nil {
		t.Fatalf("SetKV other scope failed: %v", err)
	}

	// Test GetKVScope
	disabled, err := repo.GetKVScope("disabledModels")
	if err != nil {
		t.Fatalf("GetKVScope failed: %v", err)
	}
	if len(disabled) != 2 {
		t.Fatalf("expected 2 disabled items, got %d", len(disabled))
	}
	if disabled["openai"] != `["gpt-3.5-turbo"]` || disabled["anthropic"] != `["claude-2"]` {
		t.Errorf("unexpected kv values: %v", disabled)
	}

	// Test SetKV on conflict (upsert)
	err = repo.SetKV("disabledModels", "openai", `["gpt-3.5-turbo","gpt-4-0314"]`)
	if err != nil {
		t.Fatalf("SetKV upsert failed: %v", err)
	}
	disabled, err = repo.GetKVScope("disabledModels")
	if err != nil || disabled["openai"] != `["gpt-3.5-turbo","gpt-4-0314"]` {
		t.Errorf("expected updated value on conflict, got %s (err: %v)", disabled["openai"], err)
	}

	// Test DeleteKV
	err = repo.DeleteKV("disabledModels", "openai")
	if err != nil {
		t.Fatalf("DeleteKV failed: %v", err)
	}
	disabled, err = repo.GetKVScope("disabledModels")
	if err != nil {
		t.Fatalf("GetKVScope after delete failed: %v", err)
	}
	if len(disabled) != 1 || disabled["anthropic"] == "" {
		t.Errorf("expected 1 item left in disabledModels, got %d: %v", len(disabled), disabled)
	}
}

func TestSettingsRaw(t *testing.T) {
	repo, cleanup := setupDashboardTestDB(t)
	defer cleanup()

	// Initial GetSettingsRaw on empty table should return empty map, no error
	raw, err := repo.GetSettingsRaw()
	if err != nil {
		t.Fatalf("GetSettingsRaw on empty table failed: %v", err)
	}
	if len(raw) != 0 {
		t.Fatalf("expected empty map, got %v", raw)
	}

	// UpdateSettingsRaw initial insert
	initial := map[string]any{
		"password":      "hashed_pwd",
		"requireApiKey": true,
		"rtkEnabled":    true,
	}
	err = repo.UpdateSettingsRaw(initial)
	if err != nil {
		t.Fatalf("UpdateSettingsRaw failed: %v", err)
	}

	raw, err = repo.GetSettingsRaw()
	if err != nil {
		t.Fatalf("GetSettingsRaw failed: %v", err)
	}
	if raw["password"] != "hashed_pwd" || raw["requireApiKey"] != true || raw["rtkEnabled"] != true {
		t.Errorf("unexpected settings values: %v", raw)
	}

	// Merge updates into existing settings
	updates := map[string]any{
		"rtkEnabled":      false,
		"cavemanEnabled":  true,
		"capacityAdapter": "cloud",
	}
	err = repo.UpdateSettingsRaw(updates)
	if err != nil {
		t.Fatalf("UpdateSettingsRaw merge failed: %v", err)
	}

	raw, err = repo.GetSettingsRaw()
	if err != nil {
		t.Fatalf("GetSettingsRaw after merge failed: %v", err)
	}

	// Existing fields preserved
	if raw["password"] != "hashed_pwd" {
		t.Errorf("expected password preserved, got %v", raw["password"])
	}
	if raw["requireApiKey"] != true {
		t.Errorf("expected requireApiKey preserved, got %v", raw["requireApiKey"])
	}
	// Updated fields overridden/added
	if raw["rtkEnabled"] != false {
		t.Errorf("expected rtkEnabled overridden to false, got %v", raw["rtkEnabled"])
	}
	if raw["cavemanEnabled"] != true {
		t.Errorf("expected cavemanEnabled added as true, got %v", raw["cavemanEnabled"])
	}
	if raw["capacityAdapter"] != "cloud" {
		t.Errorf("expected capacityAdapter added as cloud, got %v", raw["capacityAdapter"])
	}
}
