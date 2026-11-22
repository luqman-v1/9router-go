package chat

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

// A chat-compatible node whose stored prefix is blank must publish its models
// under the node's own id rather than under an empty namespace: a bare
// "/model" id is not a model any client can request, and it would collide
// across every prefixless node.
//
// The dashboard now fills a blank prefix at write time (suffix, then a short
// token), so create/update no longer *store* a blank. This test seeds the row
// directly because the fallback still has to hold for anything that reaches
// the table blank — a row written before the fill existed, or one written by
// a path that bypasses the handler. Without the fallback /v1/models answers
// with ids that do not resolve.
func TestHandleModels_BlankNodePrefixPublishesUnderNodeID(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	nodeID := "openai-compatible-chat-npblank"
	nodeData := `{"prefix":"","apiType":"chat","baseUrl":"https://blank.example.com/v1"}`
	if _, err := database.Exec(`INSERT INTO providerNodes (id, type, name, data, createdAt, updatedAt) VALUES
		(?, 'openai-compatible', 'No Prefix', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, nodeID, nodeData); err != nil {
		t.Fatalf("seed providerNode: %v", err)
	}

	// The connection carries the same blank prefix the node does, which is
	// what the create path writes when the operator leaves the field empty.
	customJSON := `{"providerAlias":"` + nodeID + `","id":"gp-blank","type":"chat","name":"Blank GP"}`
	if _, err := database.Exec(`INSERT INTO kv (scope, key, value) VALUES ('customModels', ? , ?)`,
		nodeID+"|gp-blank|chat", customJSON); err != nil {
		t.Fatalf("seed customModels: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		('conn-npblank', ?, 'apikey', 'Blank ID', 1, 1, '{"apiKey":"sk-x","providerSpecificData":{"prefix":""}}', '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, nodeID); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	req := httptest.NewRequest("GET", "/v1/models", nil)
	w := httptest.NewRecorder()
	handler.HandleModels(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()

	want := `"id":"` + nodeID + `/gp-blank"`
	if !strings.Contains(body, want) {
		t.Errorf("expected %s, got %s", want, body)
	}
	// The failure this guards against: an empty prefix concatenated straight
	// into the id.
	if strings.Contains(body, `"id":"/gp-blank"`) {
		t.Errorf("published a model id under an empty prefix: %s", body)
	}
	if !strings.Contains(body, `"owned_by":"`+nodeID+`"`) {
		t.Errorf("expected owned_by %q, got %s", nodeID, body)
	}
}
