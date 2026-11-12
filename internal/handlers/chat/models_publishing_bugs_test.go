package chat

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

// TestHandleModels_Bug2_EnabledModelsPinNotBypassedByCustomModels asserts that when a
// connection explicitly pins enabledModels, custom models and model aliases outside
// the pinned list do NOT bypass the allowlist.
func TestHandleModels_Bug2_EnabledModelsPinNotBypassedByCustomModels(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	for _, scope := range []string{"providerConnections", "disabledModels", "customModels", "modelAliases"} {
		if scope == "providerConnections" {
			if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
				t.Fatalf("clear connections: %v", err)
			}
			continue
		}
		if _, err := database.Exec(`DELETE FROM kv WHERE scope = ?`, scope); err != nil {
			t.Fatalf("clear %s: %v", scope, err)
		}
	}

	const (
		nodeID = "openai-compatible-chat-ec"
		prefix = "ec"
	)
	if _, err := database.Exec(
		`INSERT INTO providerNodes (id, type, name, data, createdAt, updatedAt) VALUES (?, 'openai-compatible', 'EC', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		nodeID, `{"prefix":"`+prefix+`","apiType":"openai-compatible","baseUrl":"https://api.ec.com/v1"}`,
	); err != nil {
		t.Fatalf("seed providerNode: %v", err)
	}

	// Custom models: model-pinned (allowed) and model-unpinned (not in pin).
	for _, id := range []string{"pinned-1", "unpinned-extra"} {
		if _, err := database.Exec(
			`INSERT INTO kv (scope, key, value) VALUES ('customModels', ?, ?)`,
			nodeID+"|"+id+"|llm",
			`{"providerAlias":"`+nodeID+`","id":"`+id+`","type":"llm","name":"`+id+`"}`,
		); err != nil {
			t.Fatalf("seed customModels %s: %v", id, err)
		}
	}

	// Model alias pointing to this provider but outside the pin.
	if _, err := database.Exec(`INSERT INTO kv (scope, key, value) VALUES ('modelAliases', 'ec-alias', ?)`, prefix+"/alias-extra"); err != nil {
		t.Fatalf("seed alias: %v", err)
	}

	// Connection with enabledModels pinning ONLY pinned-1.
	connData := `{"apiKey":"sk-test","enabledModels":["` + prefix + `/pinned-1"]}`
	if _, err := database.Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		 ('conn-ec', ?, 'api-key', 'EC', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		nodeID, connData,
	); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)
	req := httptest.NewRequest("GET", "/v1/models", nil)
	w := httptest.NewRecorder()
	handler.HandleModels(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	if !strings.Contains(body, `"id":"`+prefix+`/pinned-1"`) {
		t.Errorf("pinned model %s/pinned-1 must be published, got: %s", prefix, body)
	}
	if strings.Contains(body, "unpinned-extra") {
		t.Errorf("unpinned custom model must NOT bypass enabledModels pin, got: %s", body)
	}
	if strings.Contains(body, "alias-extra") {
		t.Errorf("unpinned alias target must NOT bypass enabledModels pin, got: %s", body)
	}
}

// TestHandleModels_Bug3_DisabledModelsWithProviderPrefixMatches verifies that
// disabledModels entries carrying a provider prefix (e.g. "nvidia/model-a") properly
// match stripped candidate model IDs.
func TestHandleModels_Bug3_DisabledModelsWithProviderPrefixMatches(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	for _, scope := range []string{"providerConnections", "disabledModels", "customModels"} {
		if scope == "providerConnections" {
			if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
				t.Fatalf("clear connections: %v", err)
			}
			continue
		}
		if _, err := database.Exec(`DELETE FROM kv WHERE scope = ?`, scope); err != nil {
			t.Fatalf("clear %s: %v", scope, err)
		}
	}

	const (
		nodeID = "openai-compatible-chat-nv"
		prefix = "nvidia"
	)
	if _, err := database.Exec(
		`INSERT INTO providerNodes (id, type, name, data, createdAt, updatedAt) VALUES (?, 'openai-compatible', 'NV', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		nodeID, `{"prefix":"`+prefix+`","apiType":"openai-compatible","baseUrl":"https://api.nv.com/v1"}`,
	); err != nil {
		t.Fatalf("seed providerNode: %v", err)
	}

	for _, id := range []string{"model-keep", "model-disabled"} {
		if _, err := database.Exec(
			`INSERT INTO kv (scope, key, value) VALUES ('customModels', ?, ?)`,
			prefix+"|"+id+"|llm",
			`{"providerAlias":"`+prefix+`","id":"`+id+`","type":"llm","name":"`+id+`"}`,
		); err != nil {
			t.Fatalf("seed customModels %s: %v", id, err)
		}
	}

	// Stored with provider prefix, e.g. "nvidia/model-disabled"
	disabledJSON, _ := json.Marshal([]string{"nvidia/model-disabled"})
	if _, err := database.Exec(
		`INSERT INTO kv (scope, key, value) VALUES ('disabledModels', ?, ?)`,
		prefix, string(disabledJSON),
	); err != nil {
		t.Fatalf("seed disabledModels: %v", err)
	}

	if _, err := database.Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		 ('conn-nv', ?, 'api-key', 'NV', 1, 1, '{"apiKey":"sk-test"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		nodeID,
	); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)
	req := httptest.NewRequest("GET", "/v1/models", nil)
	w := httptest.NewRecorder()
	handler.HandleModels(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	if !strings.Contains(body, `"id":"`+prefix+`/model-keep"`) {
		t.Errorf("model-keep must stay listed, got: %s", body)
	}
	if strings.Contains(body, "model-disabled") {
		t.Errorf("model-disabled must be filtered out even when stored with nvidia/ prefix, got: %s", body)
	}
}

// TestHandleModels_Bug4_NonLLMModelsExcluded asserts that STT models like
// "nvidia/parakeet-ctc-1.1b-asr" are not classified as LLM and excluded from /v1/models.
func TestHandleModels_Bug4_NonLLMModelsExcluded(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	for _, scope := range []string{"providerConnections", "disabledModels", "customModels"} {
		if scope == "providerConnections" {
			if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
				t.Fatalf("clear connections: %v", err)
			}
			continue
		}
		if _, err := database.Exec(`DELETE FROM kv WHERE scope = ?`, scope); err != nil {
			t.Fatalf("clear %s: %v", scope, err)
		}
	}

	// Active connection for nvidia
	if _, err := database.Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		 ('conn-nvidia', 'nvidia', 'api-key', 'NVIDIA', 1, 1, '{"apiKey":"nvapi-test"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
	); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)
	req := httptest.NewRequest("GET", "/v1/models", nil)
	w := httptest.NewRecorder()
	handler.HandleModels(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	if strings.Contains(body, "parakeet-ctc-1.1b-asr") {
		t.Errorf("nvidia STT model parakeet-ctc-1.1b-asr must NOT be published in /v1/models, got: %s", body)
	}
}
