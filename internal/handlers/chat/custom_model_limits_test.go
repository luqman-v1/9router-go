package chat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

// Issue #90: a custom model on an OpenAI-compatible provider node had no way to
// declare its real context window, so /v1/models published whatever the
// substring table guessed (128000/4096 for anything it did not recognise).

func findModelInfo(t *testing.T, body []byte, id string) map[string]any {
	t.Helper()
	var resp struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode /v1/models: %v (%s)", err, body)
	}
	for _, entry := range resp.Data {
		if entry["id"] == id {
			return entry
		}
	}
	t.Fatalf("model %q not published; got %s", id, body)
	return nil
}

func TestCustomModelDeclaredLimitsPublishedVerbatim(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	providers.ClearCustomModelCaps()
	t.Cleanup(providers.ClearCustomModelCaps)

	nodeData := `{"prefix":"nara","apiType":"openai-compatible","baseUrl":"https://nara.example.com/v1"}`
	if _, err := database.Exec(`INSERT INTO providerNodes (id, type, name, data, createdAt, updatedAt) VALUES
		('node-nara', 'openai-compatible', 'Nara AI', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, nodeData); err != nil {
		t.Fatalf("seed providerNode: %v", err)
	}
	// A 1M-window open-source model whose id matches no capability pattern, so
	// the substring table would answer 128000/4096.
	customJSON := `{"providerAlias":"node-nara","id":"my-custom-model","type":"llm","name":"my-custom-model","contextWindow":1000000,"maxOutput":32000}`
	if _, err := database.Exec(`INSERT INTO kv (scope, key, value) VALUES ('customModels', 'node-nara|my-custom-model|llm', ?)`, customJSON); err != nil {
		t.Fatalf("seed customModels: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-nara', 'node-nara', 'apikey', 'Nara', 1, 1, '{"apiKey":"sk-test","providerSpecificData":{"prefix":"nara"}}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	handler := NewChatHandler(db.NewRepo(database))

	w := httptest.NewRecorder()
	handler.HandleModels(w, httptest.NewRequest("GET", "/v1/models", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	entry := findModelInfo(t, w.Body.Bytes(), "nara/my-custom-model")
	if got := int(entry["context_length"].(float64)); got != 1000000 {
		t.Errorf("context_length = %d, want 1000000 (the declared value, not the table's guess)", got)
	}
	if got := int(entry["max_completion_tokens"].(float64)); got != 32000 {
		t.Errorf("max_completion_tokens = %d, want 32000", got)
	}

	caps, ok := entry["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("entry carries no capabilities block: %v", entry)
	}
	if got := int(caps["contextWindow"].(float64)); got != 1000000 {
		t.Errorf("capabilities.contextWindow = %d, want 1000000", got)
	}
	if got := int(caps["maxOutput"].(float64)); got != 32000 {
		t.Errorf("capabilities.maxOutput = %d, want 32000", got)
	}

	// /v1/models/info reads the same declaration rather than the table.
	wInfo := httptest.NewRecorder()
	handler.HandleModelsInfo(wInfo, httptest.NewRequest("GET", "/v1/models/info?id=nara/my-custom-model", nil))
	if wInfo.Code != http.StatusOK {
		t.Fatalf("models/info expected 200, got %d: %s", wInfo.Code, wInfo.Body.String())
	}
	var info map[string]any
	if err := json.Unmarshal(wInfo.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode models/info: %v", err)
	}
	if got := int(info["context_length"].(float64)); got != 1000000 {
		t.Errorf("models/info context_length = %d, want 1000000", got)
	}
	if got := int(info["max_output_tokens"].(float64)); got != 32000 {
		t.Errorf("models/info max_output_tokens = %d, want 32000", got)
	}
}

func TestCustomModelWithoutLimitsKeepsTableFallback(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	providers.ClearCustomModelCaps()
	t.Cleanup(providers.ClearCustomModelCaps)

	if _, err := database.Exec(`INSERT INTO providerNodes (id, type, name, data, createdAt, updatedAt) VALUES
		('node-nara', 'openai-compatible', 'Nara AI', '{"prefix":"nara","apiType":"openai-compatible"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed providerNode: %v", err)
	}
	// No contextWindow/maxOutput: the row must behave exactly as before.
	if _, err := database.Exec(`INSERT INTO kv (scope, key, value) VALUES ('customModels', 'node-nara|plain-model|llm', ?)`,
		`{"providerAlias":"node-nara","id":"plain-model","type":"llm","name":"plain-model"}`); err != nil {
		t.Fatalf("seed customModels: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-nara', 'node-nara', 'apikey', 'Nara', 1, 1, '{"apiKey":"sk-test","providerSpecificData":{"prefix":"nara"}}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	handler := NewChatHandler(db.NewRepo(database))
	w := httptest.NewRecorder()
	handler.HandleModels(w, httptest.NewRequest("GET", "/v1/models", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	entry := findModelInfo(t, w.Body.Bytes(), "nara/plain-model")

	wantCtx, wantMax := providers.GetModelTokenLimits("plain-model")
	if got := int(entry["context_length"].(float64)); got != wantCtx {
		t.Errorf("context_length = %d, want the table fallback %d", got, wantCtx)
	}
	if got := int(entry["max_completion_tokens"].(float64)); got != wantMax {
		t.Errorf("max_completion_tokens = %d, want the table fallback %d", got, wantMax)
	}
}

func TestCustomModelPartialLimitFillsOnlyTheGap(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	providers.ClearCustomModelCaps()
	t.Cleanup(providers.ClearCustomModelCaps)

	if _, err := database.Exec(`INSERT INTO providerNodes (id, type, name, data, createdAt, updatedAt) VALUES
		('node-nara', 'openai-compatible', 'Nara AI', '{"prefix":"nara","apiType":"openai-compatible"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed providerNode: %v", err)
	}
	// Only the window is declared; the output must still come from the table.
	if _, err := database.Exec(`INSERT INTO kv (scope, key, value) VALUES ('customModels', 'node-nara|half-declared|llm', ?)`,
		`{"providerAlias":"node-nara","id":"half-declared","type":"llm","name":"half-declared","contextWindow":512000}`); err != nil {
		t.Fatalf("seed customModels: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-nara', 'node-nara', 'apikey', 'Nara', 1, 1, '{"apiKey":"sk-test","providerSpecificData":{"prefix":"nara"}}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	handler := NewChatHandler(db.NewRepo(database))
	w := httptest.NewRecorder()
	handler.HandleModels(w, httptest.NewRequest("GET", "/v1/models", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	entry := findModelInfo(t, w.Body.Bytes(), "nara/half-declared")
	if got := int(entry["context_length"].(float64)); got != 512000 {
		t.Errorf("context_length = %d, want the declared 512000", got)
	}
	_, wantMax := providers.GetModelTokenLimits("half-declared")
	if got := int(entry["max_completion_tokens"].(float64)); got != wantMax {
		t.Errorf("max_completion_tokens = %d, want the table fallback %d", got, wantMax)
	}
}
