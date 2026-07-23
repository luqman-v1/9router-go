package db

import (
	"database/sql"
	"os"
	"testing"

	"9router/proxy/internal/handlerutil"
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
	if err != nil {
		t.Errorf("expected nil err for nonexistent alias, got: %v", err)
	}
	if val != "" {
		t.Errorf("expected empty string for nonexistent alias, got: %s", val)
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

func TestGetString(t *testing.T) {
	m := map[string]any{
		"name":     "pool-1",
		"strategy": "round-robin",
		"count":    3,
		"active":   true,
	}

	if got := handlerutil.GetString(m, "name"); got != "pool-1" {
		t.Errorf("expected 'pool-1', got '%s'", got)
	}
	if got := handlerutil.GetString(m, "strategy"); got != "round-robin" {
		t.Errorf("expected 'round-robin', got '%s'", got)
	}
	if got := handlerutil.GetString(m, "missing"); got != "" {
		t.Errorf("expected '' for missing key, got '%s'", got)
	}
	if got := handlerutil.GetString(m, "count"); got != "" {
		t.Errorf("expected '' for non-string value, got '%s'", got)
	}
	if got := handlerutil.GetString(m, "active"); got != "" {
		t.Errorf("expected '' for bool value, got '%s'", got)
	}
}

func TestNextURL_Empty(t *testing.T) {
	pool := &ProxyPool{URLs: []string{}}
	if got := pool.NextURL(); got != "" {
		t.Errorf("expected '' for empty URLs, got '%s'", got)
	}
}

func TestNextURL_SingleURL(t *testing.T) {
	pool := &ProxyPool{URLs: []string{"http://proxy1:8080"}}
	for i := 0; i < 3; i++ {
		if got := pool.NextURL(); got != "http://proxy1:8080" {
			t.Errorf("iteration %d: expected 'http://proxy1:8080', got '%s'", i, got)
		}
	}
}

func TestNextURL_RoundRobin(t *testing.T) {
	pool := &ProxyPool{URLs: []string{"http://a:8080", "http://b:8080", "http://c:8080"}}

	// atomic counter starts at 0, first AddUint64 returns 1: 1%3=1 -> b, 2%3=2 -> c, 3%3=0 -> a, 4%3=1 -> b
	expected := []string{
		"http://b:8080",
		"http://c:8080",
		"http://a:8080",
		"http://b:8080",
	}
	for i, want := range expected {
		if got := pool.NextURL(); got != want {
			t.Errorf("iteration %d: expected '%s', got '%s'", i, want, got)
		}
	}
}

func TestGetProxyPool(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS proxyPools (
		id TEXT PRIMARY KEY,
		data TEXT NOT NULL,
		isActive INTEGER DEFAULT 1
	);`)
	if err != nil {
		t.Fatalf("failed to create proxyPools table: %v", err)
	}

	_, err = db.Exec(`INSERT INTO proxyPools (id, data, isActive) VALUES (?, ?, ?)`,
		"pool-1", `{"name":"Test Pool","strategy":"round-robin","urls":["http://p1:8080","http://p2:8080","http://p3:8080"]}`, 1,
	)
	if err != nil {
		t.Fatalf("failed to insert proxy pool: %v", err)
	}

	repo := NewRepo(db)
	pool, err := repo.GetProxyPool("pool-1")
	if err != nil {
		t.Fatalf("GetProxyPool failed: %v", err)
	}
	if pool == nil {
		t.Fatal("expected non-nil pool")
	}
	if pool.ID != "pool-1" {
		t.Errorf("expected ID 'pool-1', got '%s'", pool.ID)
	}
	if pool.Name != "Test Pool" {
		t.Errorf("expected Name 'Test Pool', got '%s'", pool.Name)
	}
	if !pool.IsActive {
		t.Error("expected IsActive true")
	}
	if pool.Strategy != "round-robin" {
		t.Errorf("expected Strategy 'round-robin', got '%s'", pool.Strategy)
	}
	if len(pool.URLs) != 3 {
		t.Fatalf("expected 3 URLs, got %d", len(pool.URLs))
	}
	if pool.URLs[0] != "http://p1:8080" || pool.URLs[1] != "http://p2:8080" || pool.URLs[2] != "http://p3:8080" {
		t.Errorf("unexpected URLs: %v", pool.URLs)
	}
}

func TestGetProxyPool_DefaultStrategy(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS proxyPools (
		id TEXT PRIMARY KEY,
		data TEXT NOT NULL,
		isActive INTEGER DEFAULT 1
	);`)
	if err != nil {
		t.Fatalf("failed to create proxyPools table: %v", err)
	}

	// No strategy field -> should default to "round-robin"
	_, err = db.Exec(`INSERT INTO proxyPools (id, data, isActive) VALUES (?, ?, ?)`,
		"pool-2", `{"name":"No Strategy Pool","urls":["http://p1:8080"]}`, 1,
	)
	if err != nil {
		t.Fatalf("failed to insert proxy pool: %v", err)
	}

	repo := NewRepo(db)
	pool, err := repo.GetProxyPool("pool-2")
	if err != nil {
		t.Fatalf("GetProxyPool failed: %v", err)
	}
	if pool.Strategy != "round-robin" {
		t.Errorf("expected default Strategy 'round-robin', got '%s'", pool.Strategy)
	}
}

func TestGetProxyPool_Inactive(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS proxyPools (
		id TEXT PRIMARY KEY,
		data TEXT NOT NULL,
		isActive INTEGER DEFAULT 1
	);`)
	if err != nil {
		t.Fatalf("failed to create proxyPools table: %v", err)
	}

	_, err = db.Exec(`INSERT INTO proxyPools (id, data, isActive) VALUES (?, ?, ?)`,
		"pool-3", `{"name":"Inactive Pool","urls":["http://p1:8080"]}`, 0,
	)
	if err != nil {
		t.Fatalf("failed to insert proxy pool: %v", err)
	}

	repo := NewRepo(db)
	pool, err := repo.GetProxyPool("pool-3")
	if err != nil {
		t.Fatalf("GetProxyPool failed: %v", err)
	}
	if pool.IsActive {
		t.Error("expected IsActive false for inactive pool")
	}
}

func TestGetProxyPool_NotFound(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS proxyPools (
		id TEXT PRIMARY KEY,
		data TEXT NOT NULL,
		isActive INTEGER DEFAULT 1
	);`)
	if err != nil {
		t.Fatalf("failed to create proxyPools table: %v", err)
	}

	repo := NewRepo(db)
	pool, err := repo.GetProxyPool("nonexistent")
	if err == nil {
		t.Fatal("expected error for non-existent pool")
	}
	if pool != nil {
		t.Errorf("expected nil pool, got %+v", pool)
	}
}

// --- usage tests ---

func TestGetUsageDaily_Exists(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS usageDaily (
		dateKey TEXT PRIMARY KEY,
		data TEXT NOT NULL
	);`)
	if err != nil {
		t.Fatalf("failed to create usageDaily table: %v", err)
	}

	_, err = db.Exec(`INSERT INTO usageDaily (dateKey, data) VALUES (?, ?)`,
		"2026-07-18", `{"requests":10,"tokens":5000}`,
	)
	if err != nil {
		t.Fatalf("failed to insert usage: %v", err)
	}

	repo := NewRepo(db)
	data, err := repo.GetUsageDaily("2026-07-18")
	if err != nil {
		t.Fatalf("GetUsageDaily failed: %v", err)
	}
	if data != `{"requests":10,"tokens":5000}` {
		t.Errorf("unexpected data: '%s'", data)
	}
}

func TestGetUsageDaily_NotFound(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS usageDaily (
		dateKey TEXT PRIMARY KEY,
		data TEXT NOT NULL
	);`)
	if err != nil {
		t.Fatalf("failed to create usageDaily table: %v", err)
	}

	repo := NewRepo(db)
	_, err = repo.GetUsageDaily("2099-01-01")
	if err == nil {
		t.Error("expected error for non-existent dateKey")
	}
}

func TestInsertUsageHistory(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS usageHistory (
		timestamp TEXT,
		provider TEXT,
		model TEXT,
		connectionId TEXT,
		apiKey TEXT,
		endpoint TEXT,
		promptTokens INTEGER,
		completionTokens INTEGER,
		cost REAL,
		status TEXT,
		tokens TEXT,
		meta TEXT
	);`)
	if err != nil {
		t.Fatalf("failed to create usageHistory table: %v", err)
	}

	repo := NewRepo(db)
	err = repo.InsertUsageHistory("openai", "gpt-4", "conn-1", "key-1", "/chat", 100, 50, 0.015, "success", 150, `{"reason":"test"}`, `{"response_time":200}`)
	if err != nil {
		t.Fatalf("InsertUsageHistory failed: %v", err)
	}

	// Verify by reading back
	var provider, model, status, tokens string
	var promptTokens int
	err = db.QueryRow(`SELECT provider, model, promptTokens, status, tokens FROM usageHistory WHERE connectionId = ?`, "conn-1").Scan(&provider, &model, &promptTokens, &status, &tokens)
	if err != nil {
		t.Fatalf("failed to read back usageHistory: %v", err)
	}
	if provider != "openai" || model != "gpt-4" || promptTokens != 100 || status != "success" {
		t.Errorf("unexpected row: provider=%s model=%s promptTokens=%d status=%s", provider, model, promptTokens, status)
	}
}

func TestUpsertUsageDaily_Insert(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS usageDaily (
		dateKey TEXT PRIMARY KEY,
		data TEXT NOT NULL
	);`)
	if err != nil {
		t.Fatalf("failed to create usageDaily table: %v", err)
	}

	repo := NewRepo(db)
	err = repo.UpsertUsageDaily("2026-07-18", `{"requests":5}`)
	if err != nil {
		t.Fatalf("UpsertUsageDaily insert failed: %v", err)
	}

	data, err := repo.GetUsageDaily("2026-07-18")
	if err != nil {
		t.Fatalf("GetUsageDaily after insert failed: %v", err)
	}
	if data != `{"requests":5}` {
		t.Errorf("unexpected data: '%s'", data)
	}
}

func TestUpsertUsageDaily_Update(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS usageDaily (
		dateKey TEXT PRIMARY KEY,
		data TEXT NOT NULL
	);`)
	if err != nil {
		t.Fatalf("failed to create usageDaily table: %v", err)
	}

	repo := NewRepo(db)

	// Insert first
	err = repo.UpsertUsageDaily("2026-07-18", `{"requests":5}`)
	if err != nil {
		t.Fatalf("first upsert failed: %v", err)
	}

	// Replace
	err = repo.UpsertUsageDaily("2026-07-18", `{"requests":15,"tokens":1000}`)
	if err != nil {
		t.Fatalf("second upsert failed: %v", err)
	}

	data, err := repo.GetUsageDaily("2026-07-18")
	if err != nil {
		t.Fatalf("GetUsageDaily after update failed: %v", err)
	}
	if data != `{"requests":15,"tokens":1000}` {
		t.Errorf("expected replaced data, got '%s'", data)
	}
}

func TestInsertRequestDetail(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS requestDetails (
		id TEXT PRIMARY KEY,
		timestamp TEXT,
		provider TEXT,
		model TEXT,
		connectionId TEXT,
		status TEXT,
		data TEXT
	);`)
	if err != nil {
		t.Fatalf("failed to create requestDetails table: %v", err)
	}

	repo := NewRepo(db)
	err = repo.InsertRequestDetail("req-1", "openai", "gpt-4", "conn-1", "success", `{"prompt":"hello"}`)
	if err != nil {
		t.Fatalf("InsertRequestDetail failed: %v", err)
	}

	// Verify
	var provider, model, status, data string
	err = db.QueryRow(`SELECT provider, model, status, data FROM requestDetails WHERE id = ?`, "req-1").Scan(&provider, &model, &status, &data)
	if err != nil {
		t.Fatalf("failed to read back requestDetail: %v", err)
	}
	if provider != "openai" || model != "gpt-4" || status != "success" || data != `{"prompt":"hello"}` {
		t.Errorf("unexpected row: provider=%s model=%s status=%s data=%s", provider, model, status, data)
	}
}

func TestInsertRequestDetail_Duplicate(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS requestDetails (
		id TEXT PRIMARY KEY,
		timestamp TEXT,
		provider TEXT,
		model TEXT,
		connectionId TEXT,
		status TEXT,
		data TEXT
	);`)
	if err != nil {
		t.Fatalf("failed to create requestDetails table: %v", err)
	}

	repo := NewRepo(db)

	err = repo.InsertRequestDetail("req-1", "openai", "gpt-4", "conn-1", "success", `{"a":1}`)
	if err != nil {
		t.Fatalf("first insert failed: %v", err)
	}

	// INSERT OR IGNORE should not error on duplicate
	err = repo.InsertRequestDetail("req-1", "anthropic", "claude-3", "conn-2", "error", `{"a":2}`)
	if err != nil {
		t.Fatalf("duplicate insert should not error: %v", err)
	}

	// Original row should remain
	var provider string
	err = db.QueryRow(`SELECT provider FROM requestDetails WHERE id = ?`, "req-1").Scan(&provider)
	if err != nil {
		t.Fatalf("failed to read back: %v", err)
	}
	if provider != "openai" {
		t.Errorf("expected original provider 'openai', got '%s'", provider)
	}
}

func TestUpdateConnectionLastUsed(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	// providerConnections already created in setupTestDB
	repo := NewRepo(db)

	_, err := db.Exec(`INSERT INTO providerConnections (id, provider, authType, name, data, createdAt, updatedAt) VALUES
		('conn-upd-1', 'openai', 'apikey', 'Test Conn', '{"key":"val"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z');`)
	if err != nil {
		t.Fatalf("failed to seed providerConnection: %v", err)
	}

	err = repo.UpdateConnectionLastUsed("conn-upd-1")
	if err != nil {
		t.Fatalf("UpdateConnectionLastUsed failed: %v", err)
	}

	// Verify lastUsedAt is set and consecutiveUseCount incremented
	var lastUsedAt string
	var consecutiveUseCount int
	err = db.QueryRow(`SELECT lastUsedAt, consecutiveUseCount FROM providerConnections WHERE id = ?`, "conn-upd-1").Scan(&lastUsedAt, &consecutiveUseCount)
	if err != nil {
		t.Fatalf("failed to read back: %v", err)
	}
	if lastUsedAt == "" {
		t.Error("expected lastUsedAt to be set")
	}
	if consecutiveUseCount != 1 {
		t.Errorf("expected consecutiveUseCount 1, got %d", consecutiveUseCount)
	}
}

func TestUpdateConnectionLastUsed_Increment(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewRepo(db)

	_, err := db.Exec(`INSERT INTO providerConnections (id, provider, authType, name, data, createdAt, updatedAt) VALUES
		('conn-upd-2', 'anthropic', 'apikey', 'Test Conn 2', '{}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z');`)
	if err != nil {
		t.Fatalf("failed to seed providerConnection: %v", err)
	}

	repo.UpdateConnectionLastUsed("conn-upd-2")
	repo.UpdateConnectionLastUsed("conn-upd-2")
	repo.UpdateConnectionLastUsed("conn-upd-2")

	var consecutiveUseCount int
	err = db.QueryRow(`SELECT consecutiveUseCount FROM providerConnections WHERE id = ?`, "conn-upd-2").Scan(&consecutiveUseCount)
	if err != nil {
		t.Fatalf("failed to read back: %v", err)
	}
	if consecutiveUseCount != 3 {
		t.Errorf("expected consecutiveUseCount 3 after 3 calls, got %d", consecutiveUseCount)
	}
}

func TestUpdateConnectionLastUsed_NonExistent(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewRepo(db)

	// Update on non-existent ID should not error (UPDATE with no match is no-op)
	err := repo.UpdateConnectionLastUsed("no-such-connection")
	if err != nil {
		t.Errorf("expected no error for non-existent connection, got %v", err)
	}
}
