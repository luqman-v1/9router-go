package chat

import (
	"testing"

	"9router/proxy/internal/db"
)

// A provider node publishes its models as "<prefix>/<id>", and the prefix only
// routes the request to the node's connection. The gateway beyond the node has
// never heard of the prefix, so the body written upstream must carry the bare
// id — a sakoo gateway answered 400 "no model endpoints" to a request whose
// body still carried "bansos/gpt-6-luna".
func TestWireModelForNode(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	nodeData := `{"prefix":"bansos","apiType":"chat","baseUrl":"https://api.sakoo.biz.id"}`
	if _, err := database.Exec(`INSERT INTO providerNodes (id, type, name, data, createdAt, updatedAt) VALUES
		('openai-compatible-chat-852b35dc-2ec1-46c7-b2c4-fcd948f85caa', 'openai-compatible-chat', 'Bansos Node', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		nodeData); err != nil {
		t.Fatalf("failed to seed providerNodes: %v", err)
	}

	h := NewChatHandler(db.NewRepo(database))
	nodeID := "openai-compatible-chat-852b35dc-2ec1-46c7-b2c4-fcd948f85caa"

	tests := []struct {
		name     string
		provider string
		model    string
		want     string
	}{
		{name: "node prefix is stripped", provider: nodeID, model: "bansos/gpt-6-luna", want: "gpt-6-luna"},
		{name: "an unprefixed id passes through", provider: nodeID, model: "gpt-6-luna", want: "gpt-6-luna"},
		{name: "a foreign prefix is not stripped", provider: nodeID, model: "other/gpt-6-luna", want: "other/gpt-6-luna"},
		{name: "a longer prefix sharing text is not stripped", provider: nodeID, model: "bansosx/gpt-6-luna", want: "bansosx/gpt-6-luna"},
		{name: "a registry provider keeps vendor prefixes", provider: "openai", model: "openai/gpt-4o", want: "openai/gpt-4o"},
		{name: "a node with no registered prefix passes through", provider: "openai-compatible-chat-does-not-exist", model: "bansos/gpt-6-luna", want: "bansos/gpt-6-luna"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := h.wireModelForNode(tt.provider, tt.model); got != tt.want {
				t.Errorf("wireModelForNode(%q, %q) = %q, want %q", tt.provider, tt.model, got, tt.want)
			}
		})
	}
}
