package chat

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

// TestHandleModels_CompatibleNodeDisabledModels pins two claims about a custom
// (compatible) provider node whose connection predates the dashboard copying
// the node prefix into providerSpecificData.
//
// First, /v1/models must publish the node's public prefix, not the internal
// node row id: the prefix is the alias the resolver and the dashboard agree
// on, and clients cannot route `openai-compatible-chat-<uuid>/...`.
//
// Second, the disabledModels gate must still cover the node. The dashboard
// writes that list under the node's storage alias, which for a compatible node
// is the node id itself - GetProviderAlias returns the id unchanged because a
// node id is not in RegistryAliases. The two-key check in appendConnectionModels
// therefore has to keep consulting staticAlias even after outputAlias becomes
// the public prefix.
func TestHandleModels_CompatibleNodeDisabledModels(t *testing.T) {
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
		nodeID = "openai-compatible-chat-vsllm"
		prefix = "vsllm"
	)
	if _, err := database.Exec(
		`INSERT INTO providerNodes (id, type, name, data, createdAt, updatedAt) VALUES (?, 'openai-compatible', 'VSLLM', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		nodeID, `{"prefix":"`+prefix+`","apiType":"openai-compatible","baseUrl":"https://api.vsllm.com/v1"}`,
	); err != nil {
		t.Fatalf("seed providerNode: %v", err)
	}
	for _, id := range []string{"keep-me", "hide-me"} {
		if _, err := database.Exec(
			`INSERT INTO kv (scope, key, value) VALUES ('customModels', ?, ?)`,
			nodeID+"|"+id+"|llm",
			`{"providerAlias":"`+nodeID+`","id":"`+id+`","type":"llm","name":"`+id+`"}`,
		); err != nil {
			t.Fatalf("seed customModels %s: %v", id, err)
		}
	}
	// Keyed by the node id, which is what the dashboard's storageAlias resolves
	// to for a node.
	if _, err := database.Exec(
		`INSERT INTO kv (scope, key, value) VALUES ('disabledModels', ?, ?)`,
		nodeID, `["hide-me"]`,
	); err != nil {
		t.Fatalf("seed disabledModels: %v", err)
	}
	if _, err := database.Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		 ('conn-vsllm', ?, 'api-key', 'VSLLM', 1, 1, '{"apiKey":"sk-test"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
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
	// The node row owns the public alias even when the connection has no
	// providerSpecificData.prefix. Publishing the internal node id would give
	// clients an id the resolver never accepts.
	if !strings.Contains(body, `"id":"`+prefix+`/keep-me"`) {
		t.Errorf("%s/keep-me must stay listed, got: %s", prefix, body)
	}
	if strings.Contains(body, `"id":"`+nodeID+`/keep-me"`) {
		t.Errorf("%s/keep-me must not leak the internal node id, got: %s", nodeID, body)
	}
	// The disabled list is keyed by node id, so the gate must still consult
	// staticAlias after outputAlias becomes the public prefix.
	if strings.Contains(body, `"id":"`+prefix+`/hide-me"`) ||
		strings.Contains(body, `"id":"`+nodeID+`/hide-me"`) {
		t.Errorf("hide-me must be excluded after being disabled, got: %s", body)
	}
}

// TestHandleModels_CompatibleNodeDisabledByPrefix covers the other spelling:
// the same gate must hide the model when the disabled list is keyed by the
// node's prefix rather than its id, because an operator (or an older dashboard
// build) may have written it that way.
func TestHandleModels_CompatibleNodeDisabledByPrefix(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("clear connections: %v", err)
	}
	for _, scope := range []string{"disabledModels", "customModels"} {
		if _, err := database.Exec(`DELETE FROM kv WHERE scope = ?`, scope); err != nil {
			t.Fatalf("clear %s: %v", scope, err)
		}
	}

	const (
		nodeID = "openai-compatible-chat-nara"
		prefix = "nara"
	)
	if _, err := database.Exec(
		`INSERT INTO providerNodes (id, type, name, data, createdAt, updatedAt) VALUES (?, 'openai-compatible', 'Nara', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		nodeID, `{"prefix":"`+prefix+`","apiType":"openai-compatible"}`,
	); err != nil {
		t.Fatalf("seed providerNode: %v", err)
	}
	for _, id := range []string{"keep-me", "hide-me"} {
		if _, err := database.Exec(
			`INSERT INTO kv (scope, key, value) VALUES ('customModels', ?, ?)`,
			nodeID+"|"+id+"|llm",
			`{"providerAlias":"`+nodeID+`","id":"`+id+`","type":"llm","name":"`+id+`"}`,
		); err != nil {
			t.Fatalf("seed customModels %s: %v", id, err)
		}
	}
	if _, err := database.Exec(
		`INSERT INTO kv (scope, key, value) VALUES ('disabledModels', ?, ?)`,
		prefix, `["hide-me"]`,
	); err != nil {
		t.Fatalf("seed disabledModels: %v", err)
	}
	if _, err := database.Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		 ('conn-nara', ?, 'api-key', 'Nara', 1, 1, '{"apiKey":"sk-test","prefix":"`+prefix+`"}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
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
	if !strings.Contains(body, `"id":"`+prefix+`/keep-me"`) {
		t.Errorf("%s/keep-me must stay listed, got: %s", prefix, body)
	}
	if strings.Contains(body, `"id":"`+prefix+`/hide-me"`) {
		t.Errorf("%s/hide-me must be excluded when disabled by prefix, got: %s", prefix, body)
	}
}
