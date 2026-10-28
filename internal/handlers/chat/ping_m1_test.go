package chat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

// TestPingDoesNotRedirectToAnotherAccount pins the M1 contract: a ping pinned
// to a disabled connection must not be answered by a different account.
//
// A probe bypasses cooldown, model locks and the quota cache, but "disabled"
// is the operator's decision about the account, not a temporary condition. The
// pin was dropped anyway and the strategy picked the other connection, so a
// ping for the disabled account started the window of a healthy one and left
// the disabled account's window exactly as closed as it was.
func TestPingDoesNotRedirectToAnotherAccount(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	if _, err := database.Exec(`DELETE FROM providerConnections WHERE id IN ('conn-1', 'conn-2')`); err != nil {
		t.Fatalf("clear seeded connections: %v", err)
	}

	var healthyHits atomic.Int32
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		healthyHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-1","choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer healthy.Close()

	var disabledHits atomic.Int32
	disabled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		disabledHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-2","choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer disabled.Close()

	seedConnDB(t, database, "deepseek", "conn-healthy", "sk-1", healthy.URL)
	seedConnDB(t, database, "deepseek", "conn-disabled", "sk-2", disabled.URL)
	if _, err := database.Exec(`UPDATE providerConnections SET isActive = 0 WHERE id = 'conn-disabled'`); err != nil {
		t.Fatalf("disable connection: %v", err)
	}

	h := NewChatHandler(db.NewRepo(database))
	body := []byte(`{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`)

	err := h.PingConnection(handlerutil.WithProbeContext(context.Background()), "deepseek", "deepseek-chat", "conn-disabled", body)
	if err == nil {
		t.Fatal("PingConnection routed a disabled connection and reported success")
	}
	if got := healthyHits.Load(); got != 0 {
		t.Errorf("healthy connection was pinged %d times, want 0 (the pin must not be redirected)", got)
	}
	if got := disabledHits.Load(); got != 0 {
		t.Errorf("disabled connection was pinged %d times, want 0", got)
	}
}

// TestPingStaysOnThePinnedActiveConnection is the other half: an active pinned
// connection is still used, so the rule above does not turn the ping into a
// no-op.
func TestPingStaysOnThePinnedActiveConnection(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	if _, err := database.Exec(`DELETE FROM providerConnections WHERE id IN ('conn-1', 'conn-2')`); err != nil {
		t.Fatalf("clear seeded connections: %v", err)
	}

	var otherHits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-1","choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer other.Close()

	var pinnedHits atomic.Int32
	pinned := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pinnedHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-2","choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer pinned.Close()

	seedConnDB(t, database, "deepseek", "conn-other", "sk-1", other.URL)
	seedConnDB(t, database, "deepseek", "conn-pinned", "sk-2", pinned.URL)

	h := NewChatHandler(db.NewRepo(database))
	body := []byte(`{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`)

	if err := h.PingConnection(handlerutil.WithProbeContext(context.Background()), "deepseek", "deepseek-chat", "conn-pinned", body); err != nil {
		t.Fatalf("PingConnection on the pinned connection: %v", err)
	}
	if got := pinnedHits.Load(); got != 1 {
		t.Errorf("pinned connection hit %d times, want 1", got)
	}
	if got := otherHits.Load(); got != 0 {
		t.Errorf("other connection hit %d times, want 0", got)
	}
}
