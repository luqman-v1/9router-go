package chat

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

func modelsIDs(t *testing.T, h *ChatHandler) []string {
	t.Helper()
	req := httptest.NewRequest("GET", "/v1/models", nil)
	w := httptest.NewRecorder()
	h.HandleModels(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ids := make([]string, 0, len(resp.Data))
	for _, m := range resp.Data {
		ids = append(ids, m.ID)
	}
	return ids
}

func TestHandleModels_CustomOnlyWhenConnected(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("delete connections: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM kv WHERE scope='customModels'`); err != nil {
		t.Fatalf("delete customs: %v", err)
	}

	// One active credentialed connection: codex/cx.
	cxData := `{"prefix":"cx","apiKey":"tok-codex-test"}`
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-cx-1', 'codex', 'oauth', 'Codex Account', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, cxData); err != nil {
		t.Fatalf("seed codex: %v", err)
	}

	repo := db.NewRepo(database)
	// Custom for the connected provider: visible.
	if _, err := database.Exec(`INSERT INTO kv (scope, key, value) VALUES ('customModels', 'cx|my-custom|llm', ?)`,
		`{"providerAlias":"cx","id":"my-custom","type":"llm","name":"my-custom"}`); err != nil {
		t.Fatalf("seed custom cx: %v", err)
	}
	// Custom for a provider with no connection at all: hidden.
	if _, err := database.Exec(`INSERT INTO kv (scope, key, value) VALUES ('customModels', 'nvidia|ghost-model|llm', ?)`,
		`{"providerAlias":"nvidia","id":"ghost-model","type":"llm","name":"ghost-model"}`); err != nil {
		t.Fatalf("seed custom ghost: %v", err)
	}

	h := NewChatHandler(repo)
	ids := modelsIDs(t, h)
	joined := strings.Join(ids, "\n")
	if !strings.Contains(joined, "cx/my-custom") {
		t.Errorf("expected cx/my-custom listed, got:\n%s", joined)
	}
	for _, id := range ids {
		if strings.HasPrefix(id, "nvidia/") || strings.HasPrefix(id, "nv/") {
			t.Errorf("ghost custom %q must not be listed without a connection", id)
		}
	}
}

func TestHandleModels_CustomHiddenWhenProviderInactive(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("delete connections: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM kv WHERE scope='customModels'`); err != nil {
		t.Fatalf("delete customs: %v", err)
	}

	// Inactive (isActive=0) nvidia row: ghost stays hidden.
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-nv-off', 'nvidia', 'apikey', 'NV off', 1, 0, '{"apiKey":"sk-off"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed nvidia: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO kv (scope, key, value) VALUES ('customModels', 'nvidia|ghost-2|llm', ?)`,
		`{"providerAlias":"nvidia","id":"ghost-2","type":"llm","name":"ghost-2"}`); err != nil {
		t.Fatalf("seed custom: %v", err)
	}

	h := NewChatHandler(db.NewRepo(database))
	for _, id := range modelsIDs(t, h) {
		if strings.HasPrefix(id, "nvidia/") || strings.HasPrefix(id, "nv/") {
			t.Errorf("custom for inactive provider must not be listed, got %q", id)
		}
	}
}

func TestHandleModels_DisabledCustomExcluded(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("delete connections: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM kv WHERE scope='customModels'`); err != nil {
		t.Fatalf("delete customs: %v", err)
	}

	cxData := `{"prefix":"cx","apiKey":"tok-codex-test"}`
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-cx-1', 'codex', 'oauth', 'Codex Account', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, cxData); err != nil {
		t.Fatalf("seed codex: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO kv (scope, key, value) VALUES ('customModels', 'cx|gone|llm', ?)`,
		`{"providerAlias":"cx","id":"gone","type":"llm","name":"gone"}`); err != nil {
		t.Fatalf("seed custom: %v", err)
	}
	repo := db.NewRepo(database)
	if err := repo.SetKV("disabledModels", "cx", `["gone"]`); err != nil {
		t.Fatalf("seed disabled: %v", err)
	}

	h := NewChatHandler(repo)
	for _, id := range modelsIDs(t, h) {
		if id == "cx/gone" {
			t.Errorf("disabled custom cx/gone must be excluded")
		}
	}
}

func TestHandleModels_DisabledBuiltinExcluded(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("delete connections: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM kv WHERE scope='customModels'`); err != nil {
		t.Fatalf("delete customs: %v", err)
	}

	cxData := `{"prefix":"cx","apiKey":"tok-codex-test"}`
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-cx-1', 'codex', 'oauth', 'Codex Account', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, cxData); err != nil {
		t.Fatalf("seed codex: %v", err)
	}
	repo := db.NewRepo(database)
	if err := repo.SetKV("disabledModels", "cx", `["gpt-6-astra"]`); err != nil {
		t.Fatalf("seed disabled: %v", err)
	}

	h := NewChatHandler(repo)
	ids := modelsIDs(t, h)
	// Membership, not substring: cx/gpt-6-astra[1m] contains cx/gpt-6-astra as a
	// prefix, and disabling one must not disable the other.
	if containsID(ids, "cx/gpt-6-astra") {
		t.Errorf("disabled builtin cx/gpt-6-astra must be excluded, got:\n%s", strings.Join(ids, "\n"))
	}
	if !containsID(ids, "cx/gpt-6-astra[1m]") {
		t.Errorf("only cx/gpt-6-astra was disabled, so cx/gpt-6-astra[1m] must stay, got:\n%s", strings.Join(ids, "\n"))
	}
	if !containsID(ids, "cx/gpt-5.6-sol") {
		t.Errorf("non-disabled cx/gpt-5.6-sol must stay, got:\n%s", strings.Join(ids, "\n"))
	}
}

// containsID reports exact membership. Model ids prefix each other
// (gpt-6-astra / gpt-6-astra[1m]), so a substring check cannot tell a disabled
// id from a live one that starts with it.
func containsID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func TestHandleModels_ActiveRowWinsOverInactiveDuplicate(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("delete connections: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM kv WHERE scope='customModels'`); err != nil {
		t.Fatalf("delete customs: %v", err)
	}

	// Same provider with one active and one inactive row: the active row
	// keeps the provider serviceable, so its models must stay listed.
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-cx-on', 'codex', 'oauth', 'Codex On', 1, 1, '{"prefix":"cx","apiKey":"tok-on"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z'),
		('conn-cx-off', 'codex', 'oauth', 'Codex Off', 2, 0, '{"prefix":"cx","apiKey":"tok-off"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed codex rows: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO kv (scope, key, value) VALUES ('customModels', 'cx|custom-on|llm', ?)`,
		`{"providerAlias":"cx","id":"custom-on","type":"llm","name":"custom-on"}`); err != nil {
		t.Fatalf("seed custom: %v", err)
	}

	h := NewChatHandler(db.NewRepo(database))
	ids := modelsIDs(t, h)
	joined := strings.Join(ids, "\n")
	if !strings.Contains(joined, "cx/custom-on") {
		t.Errorf("custom model of an active connection must stay listed despite an inactive sibling row, got:\n%s", joined)
	}
}

func TestHandleModels_AllRowsInactiveHidesProvider(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("delete connections: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM kv WHERE scope='customModels'`); err != nil {
		t.Fatalf("delete customs: %v", err)
	}

	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-cx-off1', 'codex', 'oauth', 'Codex Off 1', 1, 0, '{"prefix":"cx","apiKey":"tok-1"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z'),
		('conn-cx-off2', 'codex', 'oauth', 'Codex Off 2', 2, 0, '{"prefix":"cx","apiKey":"tok-2"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed codex rows: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO kv (scope, key, value) VALUES ('customModels', 'cx|custom-off|llm', ?)`,
		`{"providerAlias":"cx","id":"custom-off","type":"llm","name":"custom-off"}`); err != nil {
		t.Fatalf("seed custom: %v", err)
	}

	h := NewChatHandler(db.NewRepo(database))
	for _, id := range modelsIDs(t, h) {
		if strings.HasPrefix(id, "cx/") {
			t.Errorf("provider with only inactive rows must publish nothing, got %q", id)
		}
	}
}

func TestIsLLMModelID(t *testing.T) {
	tests := []struct {
		modelID string
		wantLLM bool
	}{
		{modelID: "claude-sonnet-4.6", wantLLM: true},
		{modelID: "deepseek-ai/DeepSeek-V4-Flash", wantLLM: true},
		{modelID: "nvidia/nv-embedqa-e5-v5", wantLLM: false},
		{modelID: "text-embedding-3-large", wantLLM: false},
		{modelID: "tts-1-hd", wantLLM: false},
		{modelID: "openai/gpt-4o-mini-tts", wantLLM: false},
		{modelID: "gpt-image-2.5", wantLLM: false},
		{modelID: "black-forest-labs/FLUX.1-schnell", wantLLM: false},
		{modelID: "stability-ai/sdxl-turbo", wantLLM: false},
	}
	for _, tt := range tests {
		t.Run(tt.modelID, func(t *testing.T) {
			if got := isLLMModelID(tt.modelID); got != tt.wantLLM {
				t.Errorf("isLLMModelID(%q) = %v, want %v", tt.modelID, got, tt.wantLLM)
			}
		})
	}
}

// Upstream: providerSpecificData.enabledModels replaces the static catalog.
func TestHandleModels_EnabledModelsOverrideCatalog(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("delete connections: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM kv WHERE scope='customModels'`); err != nil {
		t.Fatalf("delete customs: %v", err)
	}

	connData := `{"prefix":"cx","apiKey":"tok-codex","providerSpecificData":{"enabledModels":["gpt-6-astra"]}}`
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-cx-en', 'codex', 'oauth', 'Codex Enabled', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, connData); err != nil {
		t.Fatalf("seed codex: %v", err)
	}

	h := NewChatHandler(db.NewRepo(database))
	ids := modelsIDs(t, h)
	joined := strings.Join(ids, "\n")
	if !strings.Contains(joined, "cx/gpt-6-astra") {
		t.Errorf("enabled model must be listed, got:\n%s", joined)
	}
	if strings.Contains(joined, "cx/gpt-5.6-sol") {
		t.Errorf("catalog entry outside enabledModels must not be listed, got:\n%s", joined)
	}
}

// Upstream: alias targets merge into the owning provider; the alias key itself
// is never published as a model id.
func TestHandleModels_AliasTargetMergedIntoProvider(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("delete connections: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-cc-alias', 'claude', 'apikey', 'Claude Alias', 1, 1, '{"apiKey":"sk-cc"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed claude: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO kv (scope, key, value) VALUES ('modelAliases', 'my-claude', '"cc/my-claude-alias"')`); err != nil {
		t.Fatalf("seed alias: %v", err)
	}

	h := NewChatHandler(db.NewRepo(database))
	ids := modelsIDs(t, h)
	joined := strings.Join(ids, "\n")
	if !strings.Contains(joined, "cc/my-claude-alias") {
		t.Errorf("alias target must be merged into the connected provider, got:\n%s", joined)
	}
	for _, id := range ids {
		if id == "my-claude" {
			t.Errorf("alias key must not be published as a model id")
		}
	}
}

// Upstream: combo entries come first and are owned by "combo".
func TestHandleModels_ComboOwnedByCombo(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES
		('c-1', 'llm-combo', 'llm', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		`["deepseek/deepseek-chat","groq/llama-3-70b"]`); err != nil {
		t.Fatalf("seed combo: %v", err)
	}

	req := httptest.NewRequest("GET", "/v1/models", nil)
	w := httptest.NewRecorder()
	NewChatHandler(db.NewRepo(database)).HandleModels(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []struct {
			ID      string                        `json:"id"`
			OwnedBy string                        `json:"owned_by"`
			Caps    *providers.CapabilitiesDetail `json:"capabilities"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Data) == 0 {
		t.Fatal("expected combo entry")
	}
	// Upstream pushes every combo before the first provider model.
	comboIdx, providerIdx := -1, -1
	for i, m := range resp.Data {
		if m.OwnedBy == "combo" {
			if comboIdx == -1 {
				comboIdx = i
				continue
			}
			continue
		}
		if providerIdx == -1 {
			providerIdx = i
		}
	}
	if comboIdx == -1 {
		t.Fatal("expected a combo entry owned_by 'combo'")
	}
	if providerIdx != -1 && comboIdx > providerIdx {
		t.Errorf("combos must precede provider models, combo at %d, provider at %d", comboIdx, providerIdx)
	}
	for _, m := range resp.Data {
		if m.ID != "llm-combo" {
			continue
		}
		if m.Caps == nil {
			t.Error("expected aggregated capabilities for the combo entry")
		}
	}
}

// Upstream filters the LLM list on the registry `kind`: nvidia's TTS/STT/
// embedding models must stay out even though the account is active.
func TestHandleModels_MediaKindModelsExcluded(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("delete connections: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM kv WHERE scope='customModels'`); err != nil {
		t.Fatalf("delete customs: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-nv-media', 'nvidia', 'apikey', 'NV Media', 1, 1, '{"apiKey":"nvapi-test"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed nvidia: %v", err)
	}

	h := NewChatHandler(db.NewRepo(database))
	joined := strings.Join(modelsIDs(t, h), "\n")
	// nvidia/parakeet-ctc-1.1b-asr is deliberately absent: upstream strips the
	// "nvidia/" qualifier before the kind lookup, so that vendor-prefixed id
	// misses the registry kind and falls through to the id heuristic — same
	// result on both sides.
	for _, media := range []string{"fastpitch", "tacotron2", "nv-embedqa-e5-v5"} {
		if strings.Contains(joined, media) {
			t.Errorf("media model %q must not appear in the LLM list, got:\n%s", media, joined)
		}
	}
	if !strings.Contains(joined, "minimaxai/minimax-m3") {
		t.Errorf("nvidia LLM models must stay listed, got:\n%s", joined)
	}
}
