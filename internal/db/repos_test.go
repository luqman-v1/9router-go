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
