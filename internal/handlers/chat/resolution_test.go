package chat

import (
	"encoding/json"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/shared"
)

func TestNewChatHandler_DefaultsAllOff(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	h := NewChatHandler(repo)
	if h.Repo != repo {
		t.Error("expected Repo to be set")
	}
	if h.Client == nil {
		t.Error("expected Client to be initialized")
	}
	if h.TokenSaver == nil {
		t.Fatal("expected non-nil TokenSaver config")
	}
	if h.TokenSaver.RTKEnabled() || h.TokenSaver.CavemanEnabled() || h.TokenSaver.PonytailEnabled() {
		t.Error("expected all token savers off by default")
	}
}

func TestNewChatHandler_WithConfig(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	ts := shared.NewTokenSaverConfig(true, true, false)
	h := NewChatHandler(repo, ts)
	if !h.TokenSaver.RTKEnabled() || !h.TokenSaver.CavemanEnabled() {
		t.Error("expected RTK and Caveman enabled")
	}
	if h.TokenSaver.PonytailEnabled() {
		t.Error("expected Ponytail disabled")
	}
}

func TestNewChatHandler_NilConfigIsAllOff(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	h := NewChatHandler(repo, nil)
	if h.TokenSaver.RTKEnabled() {
		t.Error("nil config should mean RTK off")
	}
}

func TestResolveModelEntry_ValidProviderSlashModel(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	info := h.resolveModelEntry("deepseek/deepseek-chat")
	if info == nil {
		t.Fatal("expected non-nil ModelInfo")
	}
	if info.Provider != "deepseek" || info.Model != "deepseek-chat" {
		t.Errorf("got %s/%s", info.Provider, info.Model)
	}
}

func TestResolveModelEntry_NoSlashReturnsNil(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	// Non-existent combo name returns nil.
	if info := h.resolveModelEntry("no-such-combo-name"); info != nil {
		t.Errorf("expected nil for non-existent combo, got %+v", info)
	}
}

func TestResolveModelEntry_NestedCombo(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	// Create inner combo: "inner-combo" → deepseek/deepseek-chat
	innerModels, _ := json.Marshal([]string{"deepseek/deepseek-chat"})
	database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES (?, ?, ?, ?, ?, ?)`,
		"inner", "inner-combo", "fallback", string(innerModels), "2026-07-19T00:00:00Z", "2026-07-19T00:00:00Z")

	// Create outer combo: "free-tier" → ["inner-combo", "deepseek/deepseek-chat"]
	outerModels, _ := json.Marshal([]string{"inner-combo", "deepseek/deepseek-chat"})
	database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES (?, ?, ?, ?, ?, ?)`,
		"outer", "free-tier", "fallback", string(outerModels), "2026-07-19T00:00:00Z", "2026-07-19T00:00:00Z")

	// resolveModelEntry("free-tier") should resolve via nested combo.
	info := h.resolveModelEntry("free-tier")
	if info == nil {
		t.Fatal("expected non-nil for nested combo name")
	}
	if info.Provider != "deepseek" {
		t.Errorf("expected provider 'deepseek', got %s", info.Provider)
	}
	if len(info.ComboModels) != 2 {
		t.Errorf("expected 2 combo models, got %d", len(info.ComboModels))
	}
	if info.ComboModels[0] != "inner-combo" {
		t.Errorf("expected first combo model 'inner-combo', got %s", info.ComboModels[0])
	}
}

func TestResolveModelEntry_AliasResolved(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	// "ds" is an alias for "deepseek"
	info := h.resolveModelEntry("ds/deepseek-chat")
	if info == nil {
		t.Fatal("expected non-nil ModelInfo for aliased provider")
	}
	if info.Provider != "deepseek" {
		t.Errorf("expected provider 'deepseek' after alias resolution, got %s", info.Provider)
	}
}

func TestResolvePrefixProvider_ResolvesConnection(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	nodeData := `{"prefix":"bn","apiType":"openai-compatible","baseUrl":"https://bn.example.com/v1/chat/completions"}`
	_, err := database.Exec(`INSERT INTO providerNodes (id, type, name, data, createdAt, updatedAt) VALUES
		('openai-compatible-chat-bn', 'openai-compatible', 'Bun Node', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, nodeData)
	if err != nil {
		t.Fatalf("seed providerNode: %v", err)
	}

	connData, _ := json.Marshal(map[string]interface{}{"apiKey": "sk-bn"})
	_, err = database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-bn', 'openai-compatible-chat-bn', 'apikey', 'Bun', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(connData))
	if err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	info := h.resolvePrefixProvider("bn", "claude-sonnet-4.5")
	if info == nil {
		t.Fatal("expected non-nil ModelInfo for prefix provider")
	}
	if info.Provider != "openai-compatible-chat-bn" {
		t.Errorf("expected provider node id, got %s", info.Provider)
	}
	if info.ConnectionID != "conn-bn" {
		t.Errorf("expected pinned connection id, got %s", info.ConnectionID)
	}
}

func TestResolvePrefixProvider_UnknownPrefixReturnsNil(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	if info := h.resolvePrefixProvider("zzz-unknown", "model"); info != nil {
		t.Errorf("expected nil for unknown prefix, got %+v", info)
	}
}

func TestResolveModel_CommonProviderFallback(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	// "deepseek-chat" has no slash/alias/combo, but deepseek has a seeded connection.
	info, err := h.resolveModel("deepseek-chat")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Provider != "deepseek" || info.Model != "deepseek-chat" {
		t.Errorf("expected deepseek/deepseek-chat via common-provider fallback, got %s/%s", info.Provider, info.Model)
	}
}

func TestResolveModel_ComboWithNestedComboName(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	// Inner combo: "inner-only" → deepseek/deepseek-chat
	innerModels, _ := json.Marshal([]string{"deepseek/deepseek-chat"})
	database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES (?, ?, ?, ?, ?, ?)`,
		"in1", "inner-only", "fallback", string(innerModels), "2026-07-19T00:00:00Z", "2026-07-19T00:00:00Z")

	// Outer combo: "combo-wombo" → ["inner-only", "deepseek/deepseek-chat"]
	outerModels, _ := json.Marshal([]string{"inner-only", "deepseek/deepseek-chat"})
	database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES (?, ?, ?, ?, ?, ?)`,
		"out1", "combo-wombo", "fallback", string(outerModels), "2026-07-19T00:00:00Z", "2026-07-19T00:00:00Z")

	info, err := h.resolveModel("combo-wombo")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if info.Provider != "deepseek" {
		t.Errorf("expected provider 'deepseek', got %s", info.Provider)
	}
	if info.Model != "deepseek-chat" {
		t.Errorf("expected model 'deepseek-chat', got %s", info.Model)
	}
	if len(info.ComboModels) != 2 {
		t.Errorf("expected 2 combo models, got %d", len(info.ComboModels))
	}
}

func TestResolveModel_UnresolvableReturnsError(t *testing.T) {
	// Use a DB with NO provider connections at all so the common-provider
	// fallback loop finds nothing and resolution genuinely fails.
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	// Remove the seeded deepseek/groq connections so no fallback provider matches.
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("delete connections: %v", err)
	}
	// Also clear aliases/combos that could resolve the bare model.
	if _, err := database.Exec(`DELETE FROM kv WHERE scope='modelAliases'`); err != nil {
		t.Fatalf("delete aliases: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM combos`); err != nil {
		t.Fatalf("delete combos: %v", err)
	}

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	_, err := h.resolveModel("gemini-unknown-model")
	if err == nil {
		t.Error("expected error for unresolvable model with no connections")
	}
}

