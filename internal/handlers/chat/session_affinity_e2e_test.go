package chat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

func TestSessionAffinity_MultiTerminalSimulation(t *testing.T) {
	// Track which connection received which request
	requestCounts := make(map[string]*atomic.Int64)
	for _, id := range []string{"conn-e2e-1", "conn-e2e-2", "conn-e2e-3"} {
		requestCounts[id] = &atomic.Int64{}
	}

	createMockServer := func(connID string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestCounts[connID].Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, `{"id":"chatcmpl-%s","choices":[{"message":{"role":"assistant","content":"resp"}}]}`, connID)
		}))
	}

	srv1 := createMockServer("conn-e2e-1")
	defer srv1.Close()
	srv2 := createMockServer("conn-e2e-2")
	defer srv2.Close()
	srv3 := createMockServer("conn-e2e-3")
	defer srv3.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	seedConnDB(t, database, "codex", "conn-e2e-1", "tok-1", srv1.URL)
	seedConnDB(t, database, "codex", "conn-e2e-2", "tok-2", srv2.URL)
	seedConnDB(t, database, "codex", "conn-e2e-3", "tok-3", srv3.URL)

	if err := repo.SetProviderStrategy("codex", db.ProviderStrategy{
		RotateStrategy: "round-robin",
	}); err != nil {
		t.Fatalf("SetProviderStrategy: %v", err)
	}

	terminals := []struct {
		name      string
		sessionID string
	}{
		{name: "Terminal 1 (Repo A)", sessionID: "sess-terminal-repo-a"},
		{name: "Terminal 2 (Repo B)", sessionID: "sess-terminal-repo-b"},
		{name: "Terminal 3 (Repo C)", sessionID: "sess-terminal-repo-c"},
	}

	terminalAssignedConn := make(map[string]string)

	// Round 1: Initialize all terminals (establish affinity)
	for _, term := range terminals {
		ctx := handlerutil.WithSessionID(context.Background(), term.sessionID)
		conn, _, err := h.GetBestConnectionWithContext(ctx, "codex", "", nil, "gpt-5.6")
		if err != nil || conn == nil {
			t.Fatalf("%s initial connection failed: %v", term.name, err)
		}
		terminalAssignedConn[term.sessionID] = conn.ID
	}

	// Verify all 3 terminals got distinct connections (even distribution)
	assignedSet := make(map[string]bool)
	for sess, connID := range terminalAssignedConn {
		if assignedSet[connID] {
			t.Errorf("expected distinct connections across terminals, but connection %q was assigned more than once (sess=%q)", connID, sess)
		}
		assignedSet[connID] = true
	}

	// Round 2..6: Run 5 concurrent turns per terminal
	const turnsPerTerminal = 5
	var wg sync.WaitGroup
	errCh := make(chan error, len(terminals)*turnsPerTerminal)

	for _, term := range terminals {
		term := term
		expectedConnID := terminalAssignedConn[term.sessionID]
		for turn := 1; turn <= turnsPerTerminal; turn++ {
			wg.Add(1)
			go func(turnNum int) {
				defer wg.Done()
				ctx := handlerutil.WithSessionID(context.Background(), term.sessionID)
				conn, _, err := h.GetBestConnectionWithContext(ctx, "codex", "", nil, "gpt-5.6")
				if err != nil {
					errCh <- fmt.Errorf("%s turn %d failed: %w", term.name, turnNum, err)
					return
				}
				if conn.ID != expectedConnID {
					errCh <- fmt.Errorf("%s turn %d routed to %s, expected pinned %s", term.name, turnNum, conn.ID, expectedConnID)
					return
				}
			}(turn)
		}
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent terminal error: %v", err)
	}

	// Non-session traffic interleaved: verify it rotates and does not hijack terminal affinities
	for i := 1; i <= 3; i++ {
		conn, _, err := h.GetBestConnectionWithContext(context.Background(), "codex", "", nil, "gpt-5.6")
		if err != nil || conn == nil {
			t.Fatalf("non-session request %d failed: %v", i, err)
		}
	}

	// Re-verify Terminal 1 is STILL pinned to its assigned connection
	ctxTerm1 := handlerutil.WithSessionID(context.Background(), terminals[0].sessionID)
	cTerm1, _, err := h.GetBestConnectionWithContext(ctxTerm1, "codex", "", nil, "gpt-5.6")
	if err != nil || cTerm1.ID != terminalAssignedConn[terminals[0].sessionID] {
		t.Errorf("Terminal 1 after non-session traffic routed to %v, want pinned %s", cTerm1, terminalAssignedConn[terminals[0].sessionID])
	}
}
