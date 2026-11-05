package chat

// Issue #840 reproduction: a connection pointed at a private/local gateway
// (a second 9router on 127.0.0.x) was refused by the SSRF guard's socket-level
// ControlContext, surfacing as a 502 to the chat client. The guard is a
// deliberate hardening — unlike upstream, Go does not leave chat upstreams
// unguarded — so the fix must be an opt-in, not a removal.
//
// These two tests pin the contrast on the same loopback httptest server: with
// ALLOW_PRIVATE_NETWORKING=false globally, a connection whose data does NOT set
// allowPrivateNetworking is refused (the bug), and one that DOES set it is
// permitted (the fix). The assertPublicUrl equivalent does not apply here;
// this is purely the socket-level guard.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"9router/proxy/internal/db"
)

func TestLocalGatewayConnectionRefusedWithoutOptIn(t *testing.T) {
	t.Setenv("ALLOW_PRIVATE_NETWORKING", "false")

	// Simulates the local gateway the operator runs on 127.0.0.1:20129.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chat-1","object":"chat.completion","model":"gw","choices":[{"index":0,"message":{"role":"assistant","content":"local reply"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	handler := NewChatHandler(repo)

	// A compatible-node style provider with a per-connection baseUrl pointing
	// at the loopback gateway, WITHOUT the opt-in.
	_, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		VALUES ('conn-gw', 'deepseek', 'apikey', 'Local Gateway', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		`{"apiKey":"sk-local","baseUrl":"`+srv.URL+`/v1/chat/completions"}`)
	if err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	body := []byte(`{"model":"deepseek/deepseek-chat","messages":[{"role":"user","content":"hi"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleChatCompletions(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 (SSRF block) without the opt-in, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "private/reserved IP blocked") {
		t.Errorf("expected SSRF block message, got %s", rec.Body.String())
	}
}

func TestLocalGatewayConnectionPermittedWithOptIn(t *testing.T) {
	t.Setenv("ALLOW_PRIVATE_NETWORKING", "false")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chat-1","object":"chat.completion","model":"gw","choices":[{"index":0,"message":{"role":"assistant","content":"local reply"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	handler := NewChatHandler(repo)

	// Same connection, but carrying the per-connection opt-in.
	_, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		VALUES ('conn-gw', 'deepseek', 'apikey', 'Local Gateway', 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		`{"apiKey":"sk-local","baseUrl":"`+srv.URL+`/v1/chat/completions","allowPrivateNetworking":true}`)
	if err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	body := []byte(`{"model":"deepseek/deepseek-chat","messages":[{"role":"user","content":"hi"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.HandleChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with the opt-in, got %d: %s", rec.Code, rec.Body.String())
	}
}
