package chat

import (
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/models"
)

// The rotation used to live in an in-memory index keyed by provider, so a
// restart sent every request back to the top account and two processes sharing
// one database advanced the same index independently. The stamp now lives in
// the row, so a fresh handler resumes exactly where the previous one stopped.
func TestApplyConnectionStrategy_SurvivesRestart(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	if _, err := database.Exec(`DELETE FROM providerConnections WHERE provider = 'rr-restart'`); err != nil {
		t.Fatalf("clean provider: %v", err)
	}
	for i, name := range []string{"conn-a", "conn-b", "conn-c"} {
		if err := repo.CreateProviderConnection(name, "rr-restart", "apikey", name, `{"apiKey":"sk-x"}`); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
		if err := repo.SetConnectionPriority(name, i+1); err != nil {
			t.Fatalf("set priority %s: %v", name, err)
		}
	}

	strat := db.ProviderStrategy{RotateStrategy: "round-robin", StickyLimit: 1}
	pick := func(h *ChatHandler) string {
		conns, err := repo.GetProviderConnections("rr-restart", true)
		if err != nil {
			t.Fatalf("read pool: %v", err)
		}
		return h.ApplyConnectionStrategy(conns, strat)[0].ID
	}

	// A first handler advances one account, then is thrown away entirely.
	first := pick(NewChatHandler(repo))
	if first != "conn-a" {
		t.Fatalf("first pick = %s, want conn-a", first)
	}

	// A brand-new handler must resume from conn-b, not restart at conn-a.
	second := pick(NewChatHandler(repo))
	if second != "conn-b" {
		t.Errorf("after restart: got %s, want conn-b (rotation must persist in the row)", second)
	}
	third := pick(NewChatHandler(repo))
	if third != "conn-c" {
		t.Errorf("third pick: got %s, want conn-c", third)
	}
}

// The least-recently-used tie-break keeps the first row in priority order when
// two stamps are equal. With second-precision timestamps every account ends up
// carrying the same stamp after one full cycle, and the rotation then locks
// onto the top account forever — precisely the reported "round robin never
// reaches the next account". Nanosecond stamps keep the ordering strict.
func TestApplyConnectionStrategy_KeepsRotatingPastFirstCycle(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	if _, err := database.Exec(`DELETE FROM providerConnections WHERE provider = 'rr-cycle'`); err != nil {
		t.Fatalf("clean provider: %v", err)
	}
	names := []string{"conn-a", "conn-b", "conn-c"}
	for i, name := range names {
		if err := repo.CreateProviderConnection(name, "rr-cycle", "apikey", name, `{"apiKey":"sk-x"}`); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
		if err := repo.SetConnectionPriority(name, i+1); err != nil {
			t.Fatalf("set priority %s: %v", name, err)
		}
	}

	strat := db.ProviderStrategy{RotateStrategy: "round-robin", StickyLimit: 1}
	want := []string{"conn-a", "conn-b", "conn-c", "conn-a", "conn-b", "conn-c"}
	for i, wantID := range want {
		conns, err := repo.GetProviderConnections("rr-cycle", true)
		if err != nil {
			t.Fatalf("read pool: %v", err)
		}
		got := NewChatHandler(repo).ApplyConnectionStrategy(conns, strat)[0].ID
		if got != wantID {
			t.Fatalf("pick %d: got %s, want %s", i+1, got, wantID)
		}
	}
}

// A disabled account must drop out of the rotation entirely and must not hold
// the sticky window: the reporter's core complaint was that disabling an
// account still left traffic on it.
func TestApplyConnectionStrategy_SkipsDisabledAccount(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	if _, err := database.Exec(`DELETE FROM providerConnections WHERE provider = 'rr-disabled'`); err != nil {
		t.Fatalf("clean provider: %v", err)
	}
	for i, name := range []string{"conn-a", "conn-b"} {
		if err := repo.CreateProviderConnection(name, "rr-disabled", "apikey", name, `{"apiKey":"sk-x"}`); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
		if err := repo.SetConnectionPriority(name, i+1); err != nil {
			t.Fatalf("set priority %s: %v", name, err)
		}
	}

	h := NewChatHandler(repo)
	// Sticky window of 5 would normally hold conn-a for five requests.
	strat := db.ProviderStrategy{RotateStrategy: "sticky", StickyLimit: 5}

	if got := h.ApplyConnectionStrategy(mustPool(t, repo, "rr-disabled"), strat)[0].ID; got != "conn-a" {
		t.Fatalf("first pick = %s, want conn-a", got)
	}

	if err := repo.SetConnectionStatus("conn-a", false); err != nil {
		t.Fatalf("disable conn-a: %v", err)
	}

	for i := range 3 {
		got := h.ApplyConnectionStrategy(mustPool(t, repo, "rr-disabled"), strat)[0].ID
		if got != "conn-b" {
			t.Fatalf("pick %d after disabling conn-a: got %s, want conn-b", i+1, got)
		}
	}
}

// A handler with no repository still has to advance its in-memory stamps. The
// rows are not persisted, so the only ordering available is what this call
// computes — and it has to move past the pool's existing stamps or every pick
// lands on the same account.
func TestApplyConnectionStrategy_RotatesWithoutRepo(t *testing.T) {
	strat := db.ProviderStrategy{RotateStrategy: "round-robin", StickyLimit: 1}
	h := NewChatHandler(nil)

	// The same row objects are reused across picks, the way the chat path reuses
	// the candidate set it just stamped: rebuilding them would throw away the
	// very state the rotation depends on.
	conns := []*models.ProviderConnection{
		{ID: "conn-a"}, {ID: "conn-b"}, {ID: "conn-c"},
	}
	for pick, want := range []string{"conn-a", "conn-b", "conn-c", "conn-a"} {
		rotated := h.ApplyConnectionStrategy(conns, strat)
		if got := rotated[0].ID; got != want {
			t.Fatalf("pick %d: got %s, want %s", pick+1, got, want)
		}
		conns = rotated
	}
}

func mustPool(t *testing.T, repo *db.Repo, provider string) []*models.ProviderConnection {
	t.Helper()
	conns, err := repo.GetProviderConnections(provider, true)
	if err != nil {
		t.Fatalf("read pool %s: %v", provider, err)
	}
	return conns
}
