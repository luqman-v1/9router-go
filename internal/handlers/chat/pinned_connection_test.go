package chat

import (
	"context"

	"testing"

	"9router/proxy/internal/db"
)

// A client-pinned connection must not serve a request when the dashboard has
// disabled it. Upstream resolves the pin inside availableConnections
// (src/sse/services/auth.js:100-148), where inactive and model-locked rows
// never match; honouring the pin unconditionally made the enable/disable
// toggle a no-op for every pinned request.
func TestGetBestConnection_PinnedConditions(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	if _, err := database.Exec(`DELETE FROM providerConnections WHERE provider = 'pinned-test'`); err != nil {
		t.Fatalf("clean provider: %v", err)
	}

	// primary is the account a client pins; secondary is the account that must
	// take over once primary becomes ineligible.
	seed := func(id string, priority int) {
		if err := repo.CreateProviderConnection(context.Background(), id, "pinned-test", "apikey", id, `{"apiKey":"sk-`+id+`"}`); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
		if err := repo.SetConnectionPriority(id, priority); err != nil {
			t.Fatalf("set priority %s: %v", id, err)
		}
	}
	seed("primary", 1)
	seed("secondary", 2)

	// 1. An active pin is honoured.
	conn, _, err := handler.getBestConnection("pinned-test", "primary", nil, "")
	if err != nil {
		t.Fatalf("active pin: unexpected error: %v", err)
	}
	if conn.ID != "primary" {
		t.Fatalf("active pin: got %s, want primary", conn.ID)
	}

	// 2. Disabling the pinned account must move the request on, exactly as
	//    turning the dashboard toggle off should.
	if err := repo.SetConnectionStatus("primary", false); err != nil {
		t.Fatalf("disable primary: %v", err)
	}
	conn, _, err = handler.getBestConnection("pinned-test", "primary", nil, "")
	if err != nil {
		t.Fatalf("disabled pin: unexpected error: %v", err)
	}
	if conn.ID != "secondary" {
		t.Errorf("disabled pin: got %s, want secondary to take over", conn.ID)
	}

	// 3. An explicit exclusion is honoured the same way.
	if err := repo.SetConnectionStatus("primary", true); err != nil {
		t.Fatalf("re-enable primary: %v", err)
	}
	conn, _, err = handler.getBestConnection("pinned-test", "primary", []string{"primary"}, "")
	if err != nil {
		t.Fatalf("excluded pin: unexpected error: %v", err)
	}
	if conn.ID != "secondary" {
		t.Errorf("excluded pin: got %s, want secondary to take over", conn.ID)
	}

	// 4. A model lock on the pinned row pushes the request off it too.
	lockKey := canonicalLockModel("pinned-test", "test-model")
	if err := repo.LockConnectionModel("primary", lockKey, 300, 0); err != nil {
		t.Fatalf("lock primary: %v", err)
	}
	conn, _, err = handler.getBestConnection("pinned-test", "primary", nil, "test-model")
	if err != nil {
		t.Fatalf("model-locked pin: unexpected error: %v", err)
	}
	if conn.ID != "secondary" {
		t.Errorf("model-locked pin: got %s, want secondary to take over", conn.ID)
	}
}

// A pinned connection must still belong to the provider the request asked for,
// otherwise provider A's credentials would be sent to provider B's upstream.
func TestGetBestConnection_PinnedForeignProviderRejected(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	// conn-1 belongs to deepseek (seeded by setupChatTestDB).
	if _, _, err := handler.getBestConnection("groq", "conn-1", nil, ""); err == nil {
		t.Fatal("expected an error when pinning a connection owned by another provider, got nil")
	}
}
