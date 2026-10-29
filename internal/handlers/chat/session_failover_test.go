package chat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

func TestFallback_RebindsAffinityOn429(t *testing.T) {
	// srv1 returns 429 Too Many Requests
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"Rate limit exceeded","type":"requests","code":"rate_limit_exceeded"}}`))
	}))
	defer srv1.Close()

	// srv2 returns 200 OK
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-test","choices":[{"message":{"role":"assistant","content":"hello"}}]}`))
	}))
	defer srv2.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	seedConnDB(t, database, "deepseek", "conn-fo-1", "sk-1", srv1.URL)
	seedConnDB(t, database, "deepseek", "conn-fo-2", "sk-2", srv2.URL)

	if err := repo.SetProviderStrategy("deepseek", db.ProviderStrategy{
		RotateStrategy: "round-robin",
	}); err != nil {
		t.Fatalf("SetProviderStrategy: %v", err)
	}

	sessionID := "sess-failover-429"
	model := "deepseek-chat"

	// Pre-bind to conn-fo-1
	h.getSessionAffinity().Bind("deepseek", model, sessionID, "conn-fo-1")
	if bound, ok := h.getSessionAffinity().Get("deepseek", model, sessionID); !ok || bound != "conn-fo-1" {
		t.Fatalf("initial binding expected conn-fo-1, got %q", bound)
	}

	ctx := handlerutil.WithSessionID(context.Background(), sessionID)
	rec := httptest.NewRecorder()
	reqBody := []byte(`{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`)

	err := h.handleAccountFallback(ctx, rec, "deepseek", model, "", reqBody, false, false, "/v1/chat/completions")
	if err != nil {
		t.Fatalf("handleAccountFallback failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from fallback, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify affinity was re-bound to conn-fo-2!
	boundAfter, ok := h.getSessionAffinity().Get("deepseek", model, sessionID)
	if !ok || boundAfter != "conn-fo-2" {
		t.Errorf("affinity after 429 failover = %q, want conn-fo-2", boundAfter)
	}
}

func TestHandleAccountFallback_MultiTurnSessionAffinity(t *testing.T) {
	var conn1Calls, conn2Calls, conn3Calls int
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn1Calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"c1","choices":[{"message":{"role":"assistant","content":"hello 1"}}]}`))
	}))
	defer srv1.Close()

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn2Calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"c2","choices":[{"message":{"role":"assistant","content":"hello 2"}}]}`))
	}))
	defer srv2.Close()

	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn3Calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"c3","choices":[{"message":{"role":"assistant","content":"hello 3"}}]}`))
	}))
	defer srv3.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	_, _ = database.Exec("DELETE FROM providerConnections")

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	seedConnDB(t, database, "deepseek", "conn-at-1", "sk-1", srv1.URL)
	seedConnDB(t, database, "deepseek", "conn-at-2", "sk-2", srv2.URL)
	seedConnDB(t, database, "deepseek", "conn-at-3", "sk-3", srv3.URL)

	if err := repo.SetProviderStrategy("deepseek", db.ProviderStrategy{
		RotateStrategy: "round-robin",
	}); err != nil {
		t.Fatalf("SetProviderStrategy: %v", err)
	}

	model := "deepseek-chat"
	reqBody := []byte(`{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`)

	ctxA := handlerutil.WithSessionID(context.Background(), "session-live-A")
	ctxB := handlerutil.WithSessionID(context.Background(), "session-live-B")

	// Session A Turn 1 -> conn1
	recA1 := httptest.NewRecorder()
	if err := h.handleAccountFallback(ctxA, recA1, "deepseek", model, "", reqBody, false, false, "/v1/chat/completions"); err != nil {
		t.Fatalf("session A turn 1 failed: %v", err)
	}
	// Session B Turn 1 -> conn2
	recB1 := httptest.NewRecorder()
	if err := h.handleAccountFallback(ctxB, recB1, "deepseek", model, "", reqBody, false, false, "/v1/chat/completions"); err != nil {
		t.Fatalf("session B turn 1 failed: %v", err)
	}

	if conn1Calls != 1 || conn2Calls != 1 || conn3Calls != 0 {
		t.Fatalf("initial turns: expected 1 on conn1, 1 on conn2, 0 on conn3, got conn1=%d, conn2=%d, conn3=%d", conn1Calls, conn2Calls, conn3Calls)
	}

	// Session A Turn 2 -> MUST route to conn1 (pinned)
	recA2 := httptest.NewRecorder()
	if err := h.handleAccountFallback(ctxA, recA2, "deepseek", model, "", reqBody, false, false, "/v1/chat/completions"); err != nil {
		t.Fatalf("session A turn 2 failed: %v", err)
	}
	if conn1Calls != 2 {
		t.Errorf("session A turn 2 did not route to conn-at-1 (pinned), conn1Calls=%d, conn2Calls=%d, conn3Calls=%d", conn1Calls, conn2Calls, conn3Calls)
	}

	// Session B Turn 2 -> MUST route to conn2 (pinned)
	recB2 := httptest.NewRecorder()
	if err := h.handleAccountFallback(ctxB, recB2, "deepseek", model, "", reqBody, false, false, "/v1/chat/completions"); err != nil {
		t.Fatalf("session B turn 2 failed: %v", err)
	}
	if conn2Calls != 2 {
		t.Errorf("session B turn 2 did not route to conn-at-2 (pinned), conn1Calls=%d, conn2Calls=%d, conn3Calls=%d", conn1Calls, conn2Calls, conn3Calls)
	}
}
