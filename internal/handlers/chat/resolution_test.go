package chat

import (
	json "encoding/json/v2"
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

	connData, _ := json.Marshal(map[string]any{"apiKey": "sk-bn"})
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

func TestResolveModel_CustomPrefixShadowing_BuiltinAlias(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	// Seed custom providerNode with prefix "oa" (collides with builtin alias "oa" -> "openai")
	oaNodeData := `{"prefix":"oa","apiType":"openai-compatible","baseUrl":"https://custom-oa.example.com/v1"}`
	_, err := database.Exec(`INSERT INTO providerNodes (id, type, name, data, createdAt, updatedAt) VALUES
		('openai-compatible-chat-f494', 'openai-compatible', 'Custom OA', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, oaNodeData)
	if err != nil {
		t.Fatalf("seed providerNode oa: %v", err)
	}

	// Seed active connection for Custom OA
	connData, _ := json.Marshal(map[string]any{"apiKey": "sk-custom-oa"})
	_, err = database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-custom-oa', 'openai-compatible-chat-f494', 'apikey', 'Custom OA Conn', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(connData))
	if err != nil {
		t.Fatalf("seed connection oa: %v", err)
	}

	// Seed custom providerNode with prefix "cc" (collides with builtin alias "cc" -> "claude")
	ccNodeData := `{"prefix":"cc","apiType":"openai-compatible","baseUrl":"https://custom-cc.example.com/v1"}`
	_, err = database.Exec(`INSERT INTO providerNodes (id, type, name, data, createdAt, updatedAt) VALUES
		('openai-compatible-chat-cc99', 'openai-compatible', 'Custom CC', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, ccNodeData)
	if err != nil {
		t.Fatalf("seed providerNode cc: %v", err)
	}
	connCCData, _ := json.Marshal(map[string]any{"apiKey": "sk-custom-cc"})
	_, err = database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-custom-cc', 'openai-compatible-chat-cc99', 'apikey', 'Custom CC Conn', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, string(connCCData))
	if err != nil {
		t.Fatalf("seed connection cc: %v", err)
	}

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	// 1. "oa/deepseek-v4-flash" must route to openai-compatible-chat-f494, NOT "openai"
	infoOA, err := h.resolveModel("oa/deepseek-v4-flash")
	if err != nil {
		t.Fatalf("resolveModel oa error: %v", err)
	}
	if infoOA.Provider != "openai-compatible-chat-f494" {
		t.Errorf("expected provider 'openai-compatible-chat-f494', got %q", infoOA.Provider)
	}
	if infoOA.ConnectionID != "conn-custom-oa" {
		t.Errorf("expected connectionID 'conn-custom-oa', got %q", infoOA.ConnectionID)
	}
	if infoOA.Model != "deepseek-v4-flash" {
		t.Errorf("expected model 'deepseek-v4-flash', got %q", infoOA.Model)
	}

	// 2. "cc/glm-5.3" must route to openai-compatible-chat-cc99, NOT "claude"
	infoCC, err := h.resolveModel("cc/glm-5.3")
	if err != nil {
		t.Fatalf("resolveModel cc error: %v", err)
	}
	if infoCC.Provider != "openai-compatible-chat-cc99" {
		t.Errorf("expected provider 'openai-compatible-chat-cc99', got %q", infoCC.Provider)
	}
	if infoCC.ConnectionID != "conn-custom-cc" {
		t.Errorf("expected connectionID 'conn-custom-cc', got %q", infoCC.ConnectionID)
	}

	// 3. Bare alias check "oa" resolves to custom provider node
	infoBare, err := h.resolveModel("oa")
	if err != nil {
		t.Fatalf("resolveModel bare 'oa' error: %v", err)
	}
	if infoBare.Provider != "openai-compatible-chat-f494" {
		t.Errorf("expected bare 'oa' to resolve to custom node, got %q", infoBare.Provider)
	}
}

func TestResolveModel_CustomPrefix_NoConnections_ReportsNodeID(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	// Seed custom providerNode with prefix "oa", but NO connections anywhere
	oaNodeData := `{"prefix":"oa","apiType":"openai-compatible","baseUrl":"https://custom-oa.example.com/v1"}`
	_, err := database.Exec(`INSERT INTO providerNodes (id, type, name, data, createdAt, updatedAt) VALUES
		('openai-compatible-chat-f494', 'openai-compatible', 'Custom OA', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, oaNodeData)
	if err != nil {
		t.Fatalf("seed providerNode oa: %v", err)
	}

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	// Since openai has no connections, resolveModel should pin to custom node ID
	info, err := h.resolveModel("oa/deepseek-v4-flash")
	if err != nil {
		t.Fatalf("resolveModel error: %v", err)
	}
	if info.Provider != "openai-compatible-chat-f494" {
		t.Errorf("expected provider 'openai-compatible-chat-f494' (not shadowed 'openai'), got %q", info.Provider)
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
	if len(info.ComboModels) != 1 || info.ComboModels[0] != "deepseek/deepseek-chat" {
		t.Errorf("expected flattened [deepseek/deepseek-chat], got %v", info.ComboModels)
	}
}

func TestFlattenComboModels_ExpandsNested(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	inner, _ := json.Marshal([]string{"oc/ling-3.0-flash-free", "gemini/gemini-3.5-flash"})
	database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES (?, ?, ?, ?, ?, ?)`,
		"ft1", "free-tier", "fallback", string(inner), "2026-07-19T00:00:00Z", "2026-07-19T00:00:00Z")

	flat, err := h.flattenComboModels([]string{"free-tier", "openai/gpt-4"})
	if err != nil {
		t.Fatalf("flatten error: %v", err)
	}
	want := []string{"oc/ling-3.0-flash-free", "gemini/gemini-3.5-flash", "openai/gpt-4"}
	if len(flat) != len(want) {
		t.Fatalf("expected %d models, got %d: %v", len(want), len(flat), flat)
	}
	for i := range want {
		if flat[i] != want[i] {
			t.Errorf("expected %v, got %v", want, flat)
			break
		}
	}
}

func TestFlattenComboModels_DeeplyNested(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	// base-combo -> ["ag/gemini-3.7-flash-high", "ag/gemini-pro-agent"]
	base, _ := json.Marshal([]string{"ag/gemini-3.7-flash-high", "ag/gemini-pro-agent"})
	database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES (?, ?, ?, ?, ?, ?)`,
		"c-base", "base-combo", "fallback", string(base), "2026-07-19T00:00:00Z", "2026-07-19T00:00:00Z")

	// mid-combo -> ["base-combo", "oc/deepseek-v4-flash-free"]
	mid, _ := json.Marshal([]string{"base-combo", "oc/deepseek-v4-flash-free"})
	database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES (?, ?, ?, ?, ?, ?)`,
		"c-mid", "mid-combo", "fallback", string(mid), "2026-07-19T00:00:00Z", "2026-07-19T00:00:00Z")

	// super-combo -> ["mid-combo", "openai/gpt-4o"]
	flat, err := h.flattenComboModels([]string{"mid-combo", "openai/gpt-4o"})
	if err != nil {
		t.Fatalf("flatten error: %v", err)
	}

	want := []string{"ag/gemini-3.7-flash-high", "ag/gemini-pro-agent", "oc/deepseek-v4-flash-free", "openai/gpt-4o"}
	if len(flat) != len(want) {
		t.Fatalf("expected %d models, got %d: %v", len(want), len(flat), flat)
	}
	for i := range want {
		if flat[i] != want[i] {
			t.Errorf("at index %d: expected %q, got %q", i, want[i], flat[i])
		}
	}
}

func TestFlattenComboModels_DedupesConsecutive(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	inner, _ := json.Marshal([]string{"deepseek/deepseek-chat"})
	database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES (?, ?, ?, ?, ?, ?)`,
		"in1", "inner-only", "fallback", string(inner), "2026-07-19T00:00:00Z", "2026-07-19T00:00:00Z")

	// ["inner-only", "deepseek/deepseek-chat"] both expand to the same leaf.
	flat, err := h.flattenComboModels([]string{"inner-only", "deepseek/deepseek-chat"})
	if err != nil {
		t.Fatalf("flatten error: %v", err)
	}
	if len(flat) != 1 || flat[0] != "deepseek/deepseek-chat" {
		t.Errorf("expected [deepseek/deepseek-chat], got %v", flat)
	}
}

func TestFlattenComboModels_Cycle(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	a, _ := json.Marshal([]string{"combo-b"})
	b, _ := json.Marshal([]string{"combo-a"})
	database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES (?, ?, ?, ?, ?, ?)`,
		"ca", "combo-a", "fallback", string(a), "2026-07-19T00:00:00Z", "2026-07-19T00:00:00Z")
	database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES (?, ?, ?, ?, ?, ?)`,
		"cb", "combo-b", "fallback", string(b), "2026-07-19T00:00:00Z", "2026-07-19T00:00:00Z")

	if _, err := h.flattenComboModels([]string{"combo-a"}); err == nil {
		t.Fatal("expected cycle error when no concrete models remain")
	}
}

func TestFlattenComboModels_GracefulCycleRecovery(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	// combo-self contains ["combo-self", "antigravity/gemini-3.7-flash"]
	selfList, _ := json.Marshal([]string{"combo-self", "antigravity/gemini-3.7-flash"})
	database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES (?, ?, ?, ?, ?, ?)`,
		"cself", "combo-self", "fallback", string(selfList), "2026-07-19T00:00:00Z", "2026-07-19T00:00:00Z")

	flat, err := h.flattenComboModels([]string{"combo-self"})
	if err != nil {
		t.Fatalf("expected graceful cycle recovery, got error: %v", err)
	}

	if len(flat) != 1 || flat[0] != "antigravity/gemini-3.7-flash" {
		t.Fatalf("expected [antigravity/gemini-3.7-flash], got %v", flat)
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

func TestResolveModel_BareCodexAutoReview(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	info, err := h.resolveModel("codex-auto-review")
	if err != nil {
		t.Fatalf("expected codex-auto-review to resolve, got error: %v", err)
	}
	if info.Provider != "codex" || info.Model != "codex-auto-review" {
		t.Errorf("expected Provider=codex, Model=codex-auto-review, got Provider=%s, Model=%s", info.Provider, info.Model)
	}
}

func TestResolveModel_StandardPrefixes(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	// oc/ prefix resolves to opencode
	info1, err := h.resolveModel("oc/space-bunny-free")
	if err != nil {
		t.Fatalf("expected oc/space-bunny-free to resolve, got error: %v", err)
	}
	if info1.Provider != "opencode" || info1.Model != "space-bunny-free" {
		t.Errorf("expected Provider=opencode, Model=space-bunny-free, got %+v", info1)
	}

	// ag/ prefix resolves to antigravity
	info2, err := h.resolveModel("ag/gemini-3.8-flash-high")
	if err != nil {
		t.Fatalf("expected ag/gemini-3.8-flash-high to resolve, got error: %v", err)
	}
	if info2.Provider != "antigravity" || info2.Model != "gemini-3.8-flash-high" {
		t.Errorf("expected Provider=antigravity, Model=gemini-3.8-flash-high, got %+v", info2)
	}

	// antigravity/ prefix resolves to antigravity
	info3, err := h.resolveModel("antigravity/gemini-3.8-flash")
	if err != nil {
		t.Fatalf("expected antigravity/gemini-3.8-flash to resolve, got error: %v", err)
	}
	if info3.Provider != "antigravity" || info3.Model != "gemini-3.8-flash" {
		t.Errorf("expected Provider=antigravity, Model=gemini-3.8-flash, got %+v", info3)
	}
}

func TestResolveModel_StrictProviderIsolation(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	h := NewChatHandler(db.NewRepo(database))

	// ag/ prefix must strictly resolve to antigravity, never hijack to opencode
	info, err := h.resolveModel("ag/muse-spark-1.3-contributor-free")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Provider != "antigravity" || info.Model != "muse-spark-1.3-contributor-free" {
		t.Fatalf("expected antigravity/muse-spark-1.3-contributor-free, got %s/%s", info.Provider, info.Model)
	}

	entry := h.resolveModelEntry("ag/muse-spark-1.3-contributor-free")
	if entry == nil || entry.Provider != "antigravity" {
		t.Fatalf("resolveModelEntry: expected antigravity, got %+v", entry)
	}
}
