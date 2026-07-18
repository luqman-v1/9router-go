package db

import (
	"database/sql"
	"os"
	"testing"
)

func setupTestDB(t *testing.T) (*sql.DB, func()) {
	tmpFile, err := os.CreateTemp("", "test_repos_*.sqlite")
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

	// Create tables according to the actual schema
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
		`CREATE TABLE providerNodes (
			id TEXT PRIMARY KEY,
			type TEXT,
			name TEXT,
			data TEXT NOT NULL,
			createdAt TEXT NOT NULL,
			updatedAt TEXT NOT NULL
		);`,
	}

	for _, query := range schema {
		if _, err := db.Exec(query); err != nil {
			cleanup()
			t.Fatalf("failed to create table: %v", err)
		}
	}

	return db, cleanup
}

func TestValidateApiKey(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	// Seed data
	_, err := db.Exec(`INSERT INTO apiKeys (id, key, name, machineId, isActive, createdAt) VALUES
		('1', 'valid-key', 'Test Key 1', 'mac-1', 1, '2026-07-18T00:00:00Z'),
		('2', 'inactive-key', 'Test Key 2', 'mac-2', 0, '2026-07-18T00:00:00Z');`)
	if err != nil {
		t.Fatalf("failed to seed apiKeys: %v", err)
	}

	repo := NewRepo(db)

	// Test valid key
	valid, err := repo.ValidateApiKey("valid-key")
	if err != nil {
		t.Errorf("ValidateApiKey returned error: %v", err)
	}
	if !valid {
		t.Error("expected valid-key to be valid")
	}

	// Test inactive key
	inactive, err := repo.ValidateApiKey("inactive-key")
	if err != nil {
		t.Errorf("ValidateApiKey returned error: %v", err)
	}
	if inactive {
		t.Error("expected inactive-key to be invalid")
	}

	// Test non-existent key
	nonexistent, err := repo.ValidateApiKey("nonexistent-key")
	if err != nil {
		t.Errorf("ValidateApiKey returned error: %v", err)
	}
	if nonexistent {
		t.Error("expected nonexistent-key to be invalid")
	}

	// Test GetApiKeyByKey
	keyObj, err := repo.GetApiKeyByKey("valid-key")
	if err != nil {
		t.Errorf("GetApiKeyByKey returned error: %v", err)
	}
	if keyObj == nil {
		t.Fatal("expected key details to be returned")
	}
	if keyObj.ID != "1" || keyObj.Key != "valid-key" || *keyObj.Name != "Test Key 1" || keyObj.IsActive != 1 {
		t.Errorf("unexpected APIKey fields: %+v", keyObj)
	}
}

func TestGetProviderConnections(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	// Seed data
	_, err := db.Exec(`INSERT INTO providerConnections (id, provider, authType, name, email, priority, isActive, data, createdAt, updatedAt) VALUES
		('1', 'openai', 'apikey', 'OpenAI 1', 'openai1@test.com', 2, 1, '{"apiKey": "op1"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z'),
		('2', 'openai', 'apikey', 'OpenAI 2', 'openai2@test.com', 1, 1, '{"apiKey": "op2"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z'),
		('3', 'openai', 'apikey', 'OpenAI Inactive', 'openai3@test.com', 3, 0, '{"apiKey": "op3"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z'),
		('4', 'anthropic', 'apikey', 'Anthropic 1', 'anthropic@test.com', NULL, 1, '{"apiKey": "ant"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z');`)
	if err != nil {
		t.Fatalf("failed to seed providerConnections: %v", err)
	}

	repo := NewRepo(db)

	// Get all OpenAI active connections (should be sorted by priority ASC)
	conns, err := repo.GetProviderConnections("openai", true)
	if err != nil {
		t.Errorf("GetProviderConnections failed: %v", err)
	}
	if len(conns) != 2 {
		t.Fatalf("expected 2 active connections, got %d", len(conns))
	}
	// Priority 1 should be first, then priority 2
	if conns[0].ID != "2" || conns[1].ID != "1" {
		t.Errorf("expected sorted connections: conns[0].ID=%s (expected 2), conns[1].ID=%s (expected 1)", conns[0].ID, conns[1].ID)
	}

	// Get all OpenAI connections (including inactive)
	allConns, err := repo.GetProviderConnections("openai", false)
	if err != nil {
		t.Errorf("GetProviderConnections failed: %v", err)
	}
	if len(allConns) != 3 {
		t.Fatalf("expected 3 connections, got %d", len(allConns))
	}

	// Get Anthropic connections (testing priority NULL -> 999999 sorting)
	antConns, err := repo.GetProviderConnections("anthropic", true)
	if err != nil {
		t.Errorf("GetProviderConnections failed: %v", err)
	}
	if len(antConns) != 1 {
		t.Fatalf("expected 1 anthropic connection, got %d", len(antConns))
	}
	if antConns[0].Priority != nil {
		t.Errorf("expected null priority, got %d", *antConns[0].Priority)
	}

	// Get all active connections across all providers
	allActive, err := repo.GetProviderConnections("", true)
	if err != nil {
		t.Errorf("GetProviderConnections failed: %v", err)
	}
	if len(allActive) != 3 {
		t.Errorf("expected 3 active connections total, got %d", len(allActive))
	}

	// Get all connections across all providers (active and inactive)
	allTotal, err := repo.GetProviderConnections("", false)
	if err != nil {
		t.Errorf("GetProviderConnections failed: %v", err)
	}
	if len(allTotal) != 4 {
		t.Errorf("expected 4 connections total, got %d", len(allTotal))
	}
}

func TestModelAliases(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	// Seed data (simulating JSON strings and raw strings)
	_, err := db.Exec(`INSERT INTO kv (scope, key, value) VALUES
		('modelAliases', 'gpt-4', '"gpt-4o"'),
		('modelAliases', 'claude-3', '"claude-3-5-sonnet"'),
		('modelAliases', 'raw-model', 'gpt-4-raw'),
		('otherScope', 'gpt-4', '"other-val"');`)
	if err != nil {
		t.Fatalf("failed to seed kv: %v", err)
	}

	repo := NewRepo(db)

	// Test GetModelAlias
	val, err := repo.GetModelAlias("gpt-4")
	if err != nil {
		t.Errorf("GetModelAlias failed: %v", err)
	}
	if val != "gpt-4o" {
		t.Errorf("expected gpt-4o, got %s", val)
	}

	// Test GetModelAlias with raw string (non-JSON string value)
	val, err = repo.GetModelAlias("raw-model")
	if err != nil {
		t.Errorf("GetModelAlias failed: %v", err)
	}
	if val != "gpt-4-raw" {
		t.Errorf("expected gpt-4-raw, got %s", val)
	}

	// Test nonexistent alias
	val, err = repo.GetModelAlias("nonexistent")
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows, got err: %v, val: %s", err, val)
	}

	// Test GetModelAliases (all aliases)
	aliases, err := repo.GetModelAliases()
	if err != nil {
		t.Errorf("GetModelAliases failed: %v", err)
	}
	if len(aliases) != 3 {
		t.Errorf("expected 3 aliases, got %d", len(aliases))
	}
	if aliases["gpt-4"] != "gpt-4o" || aliases["claude-3"] != "claude-3-5-sonnet" || aliases["raw-model"] != "gpt-4-raw" {
		t.Errorf("unexpected aliases mapping: %+v", aliases)
	}
}

func TestCombos(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	// Seed data
	_, err := db.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES
		('c1', 'combo-1', 'chat', '[{"model":"gpt-4o","weight":1}]', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z'),
		('c2', 'combo-2', NULL, '[]', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z');`)
	if err != nil {
		t.Fatalf("failed to seed combos: %v", err)
	}

	repo := NewRepo(db)

	// Test GetComboByName
	combo, err := repo.GetComboByName("combo-1")
	if err != nil {
		t.Errorf("GetComboByName failed: %v", err)
	}
	if combo == nil || combo.ID != "c1" || combo.Models != `[{"model":"gpt-4o","weight":1}]` {
		t.Errorf("unexpected combo by name: %+v", combo)
	}

	// Test GetComboById
	combo, err = repo.GetComboById("c2")
	if err != nil {
		t.Errorf("GetComboById failed: %v", err)
	}
	if combo == nil || combo.Name != "combo-2" || combo.Kind != nil {
		t.Errorf("unexpected combo by id: %+v", combo)
	}

	// Test GetCombos
	combos, err := repo.GetCombos()
	if err != nil {
		t.Errorf("GetCombos failed: %v", err)
	}
	if len(combos) != 2 {
		t.Errorf("expected 2 combos, got %d", len(combos))
	}
}

func TestGetProviderNodeByID(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	nodeData := `{"prefix":"bn","apiType":"openai-compatible","baseUrl":"https://custom.example.com/v1/chat/completions"}`
	_, err := db.Exec(`INSERT INTO providerNodes (id, type, name, data, createdAt, updatedAt) VALUES
		('openai-compatible-chat-abc123', 'openai-compatible', 'Bun Node', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		nodeData)
	if err != nil {
		t.Fatalf("failed to seed providerNodes: %v", err)
	}

	repo := NewRepo(db)

	// Existing node
	node, data, err := repo.GetProviderNodeByID("openai-compatible-chat-abc123")
	if err != nil {
		t.Fatalf("GetProviderNodeByID returned error: %v", err)
	}
	if node == nil {
		t.Fatal("expected node, got nil")
	}
	if node.ID != "openai-compatible-chat-abc123" {
		t.Errorf("expected ID 'openai-compatible-chat-abc123', got '%s'", node.ID)
	}
	if data == nil {
		t.Fatal("expected data, got nil")
	}
	if data.Prefix != "bn" {
		t.Errorf("expected prefix 'bn', got '%s'", data.Prefix)
	}
	if data.BaseURL != "https://custom.example.com/v1/chat/completions" {
		t.Errorf("expected baseUrl, got '%s'", data.BaseURL)
	}

	// Non-existent node
	node, data, err = repo.GetProviderNodeByID("nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if node != nil {
		t.Errorf("expected nil for nonexistent node, got %+v", node)
	}
	if data != nil {
		t.Errorf("expected nil data for nonexistent node, got %+v", data)
	}
}

func TestGetProviderNodeByPrefix(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	node1Data := `{"prefix":"bn","apiType":"openai-compatible","baseUrl":"https://bn.example.com/v1/chat/completions"}`
	node2Data := `{"prefix":"cf","apiType":"openai-compatible","baseUrl":"https://cf.example.com/v1/chat/completions"}`
	_, err := db.Exec(`INSERT INTO providerNodes (id, type, name, data, createdAt, updatedAt) VALUES
		('node-bn', 'openai-compatible', 'Bun Node', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z'),
		('node-cf', 'openai-compatible', 'CF Node', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		node1Data, node2Data)
	if err != nil {
		t.Fatalf("failed to seed providerNodes: %v", err)
	}

	repo := NewRepo(db)

	// Match by prefix "bn"
	node, data, err := repo.GetProviderNodeByPrefix("bn")
	if err != nil {
		t.Fatalf("GetProviderNodeByPrefix returned error: %v", err)
	}
	if node == nil {
		t.Fatal("expected node for prefix 'bn', got nil")
	}
	if node.ID != "node-bn" {
		t.Errorf("expected ID 'node-bn', got '%s'", node.ID)
	}
	if data.Prefix != "bn" {
		t.Errorf("expected prefix 'bn', got '%s'", data.Prefix)
	}

	// Match by prefix "cf"
	node, data, err = repo.GetProviderNodeByPrefix("cf")
	if err != nil {
		t.Fatalf("GetProviderNodeByPrefix returned error: %v", err)
	}
	if node == nil || node.ID != "node-cf" {
		t.Errorf("expected node-cf, got %+v", node)
	}

	// Non-existent prefix
	node, data, err = repo.GetProviderNodeByPrefix("zz")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if node != nil {
		t.Errorf("expected nil for non-existent prefix, got %+v", node)
	}
}

func TestGetProviderConnectionByID(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	_, err := db.Exec(`INSERT INTO providerConnections (id, provider, authType, name, email, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-abc', 'node-bn', 'apikey', 'Bun Connection', NULL, 1, 1, '{"apiKey":"sk-bn"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`)
	if err != nil {
		t.Fatalf("failed to seed providerConnections: %v", err)
	}

	repo := NewRepo(db)

	// Existing connection
	conn, err := repo.GetProviderConnectionByID("conn-abc")
	if err != nil {
		t.Fatalf("GetProviderConnectionByID returned error: %v", err)
	}
	if conn == nil {
		t.Fatal("expected connection, got nil")
	}
	if conn.ID != "conn-abc" {
		t.Errorf("expected ID 'conn-abc', got '%s'", conn.ID)
	}
	if conn.Provider != "node-bn" {
		t.Errorf("expected provider 'node-bn', got '%s'", conn.Provider)
	}

	// Non-existent connection
	conn, err = repo.GetProviderConnectionByID("nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conn != nil {
		t.Errorf("expected nil for nonexistent connection, got %+v", conn)
	}
}

func TestLockModel(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewRepo(db)

	// Lock a model
	err := repo.LockModel("deepseek", "deepseek-chat", 120, "401 Unauthorized", 401)
	if err != nil {
		t.Fatalf("LockModel failed: %v", err)
	}

	// Verify it's locked
	locked, err := repo.IsModelLocked("deepseek", "deepseek-chat")
	if err != nil {
		t.Fatalf("IsModelLocked failed: %v", err)
	}
	if !locked {
		t.Error("expected model to be locked")
	}

	// Verify lock details
	lock, err := repo.GetModelLock("deepseek", "deepseek-chat")
	if err != nil {
		t.Fatalf("GetModelLock failed: %v", err)
	}
	if lock == nil {
		t.Fatal("expected lock details, got nil")
	}
	if lock.LastError != "401 Unauthorized" {
		t.Errorf("expected lastError '401 Unauthorized', got '%s'", lock.LastError)
	}
	if lock.ErrorCode != 401 {
		t.Errorf("expected errorCode 401, got %d", lock.ErrorCode)
	}

	// Key should be uppercase
	var rawKey string
	err = db.QueryRow("SELECT key FROM kv WHERE scope = 'modelLock' LIMIT 1").Scan(&rawKey)
	if err != nil {
		t.Fatalf("failed to query lock key: %v", err)
	}
	if rawKey != "DEEPSEEK/DEEPSEEK-CHAT" {
		t.Errorf("expected uppercase key 'DEEPSEEK/DEEPSEEK-CHAT', got '%s'", rawKey)
	}
}

func TestIsModelLocked_NotLocked(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewRepo(db)

	locked, err := repo.IsModelLocked("openai", "gpt-4o")
	if err != nil {
		t.Fatalf("IsModelLocked failed: %v", err)
	}
	if locked {
		t.Error("expected model to not be locked")
	}
}

func TestUnlockModel(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewRepo(db)

	// Lock then unlock
	err := repo.LockModel("groq", "llama-3", 60, "429 Too Many Requests", 429)
	if err != nil {
		t.Fatalf("LockModel failed: %v", err)
	}

	locked, _ := repo.IsModelLocked("groq", "llama-3")
	if !locked {
		t.Fatal("expected model to be locked before unlock")
	}

	err = repo.UnlockModel("groq", "llama-3")
	if err != nil {
		t.Fatalf("UnlockModel failed: %v", err)
	}

	locked, err = repo.IsModelLocked("groq", "llama-3")
	if err != nil {
		t.Fatalf("IsModelLocked failed: %v", err)
	}
	if locked {
		t.Error("expected model to be unlocked after UnlockModel")
	}
}

func TestLockModel_Replace(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewRepo(db)

	// Lock with 401
	err := repo.LockModel("deepseek", "deepseek-chat", 120, "401 Unauthorized", 401)
	if err != nil {
		t.Fatalf("LockModel failed: %v", err)
	}

	// Replace with 429
	err = repo.LockModel("deepseek", "deepseek-chat", 60, "429 Rate Limited", 429)
	if err != nil {
		t.Fatalf("LockModel replace failed: %v", err)
	}

	lock, err := repo.GetModelLock("deepseek", "deepseek-chat")
	if err != nil {
		t.Fatalf("GetModelLock failed: %v", err)
	}
	if lock == nil {
		t.Fatal("expected lock after replace")
	}
	if lock.ErrorCode != 429 {
		t.Errorf("expected replaced errorCode 429, got %d", lock.ErrorCode)
	}
	if lock.LastError != "429 Rate Limited" {
		t.Errorf("expected replaced lastError '429 Rate Limited', got '%s'", lock.LastError)
	}
}

func TestGetModelLock_Expired(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewRepo(db)

	// Insert a lock with 0 duration (already expired)
	err := repo.LockModel("deepseek", "deepseek-chat", 0, "test", 401)
	if err != nil {
		t.Fatalf("LockModel failed: %v", err)
	}

	// GetModelLock should return nil for expired locks
	lock, err := repo.GetModelLock("deepseek", "deepseek-chat")
	if err != nil {
		t.Fatalf("GetModelLock failed: %v", err)
	}
	if lock != nil {
		t.Error("expected nil for expired lock")
	}

	// IsModelLocked should return false for expired locks
	locked, err := repo.IsModelLocked("deepseek", "deepseek-chat")
	if err != nil {
		t.Fatalf("IsModelLocked failed: %v", err)
	}
	if locked {
		t.Error("expected expired lock to be treated as unlocked")
	}
}
