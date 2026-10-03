package usagetracker

import (
	"path/filepath"
	"testing"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/dbtest"
)

func TestTracker_Lifecycle(t *testing.T) {
	tracker := NewTracker()

	// Initially empty
	state := tracker.GetActiveState(nil)
	if len(state.ActiveRequests) != 0 {
		t.Errorf("expected 0 active requests, got %d", len(state.ActiveRequests))
	}

	// 1. Request starts
	tracker.TrackPending("claude-sonnet-4-6", "anthropic", "conn_123", true, false)
	state = tracker.GetActiveState(nil)
	if len(state.ActiveRequests) != 1 {
		t.Fatalf("expected 1 active request, got %d", len(state.ActiveRequests))
	}
	if state.ActiveRequests[0].Provider != "anthropic" {
		t.Errorf("expected provider anthropic, got %s", state.ActiveRequests[0].Provider)
	}

	// 2. Request finishes with success and push recent
	tracker.TrackPending("claude-sonnet-4-6", "anthropic", "conn_123", false, false)
	tracker.PushRecent(RecentRequest{
		Timestamp:        time.Now().UTC().Format(time.RFC3339),
		Model:            "claude-sonnet-4-6",
		Provider:         "anthropic",
		PromptTokens:     100,
		CompletionTokens: 50,
		Status:           "success",
	}, nil)

	state = tracker.GetActiveState(nil)
	if len(state.ActiveRequests) != 0 {
		t.Errorf("expected 0 active requests after completion, got %d", len(state.ActiveRequests))
	}
	if len(state.RecentRequests) != 1 {
		t.Errorf("expected 1 recent request, got %d", len(state.RecentRequests))
	}
}

func TestTracker_Subscription(t *testing.T) {
	tracker := NewTracker()
	ch, unsub := tracker.Subscribe()
	defer unsub()

	tracker.TrackPending("gpt-4o", "openai", "conn_abc", true, false)

	select {
	case payload := <-ch:
		if len(payload) == 0 {
			t.Error("received empty payload")
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("timed out waiting for subscription broadcast")
	}
}

// The SSE stream carries the in-memory ring, which starts empty after a
// restart. The dashboard loads 20 DB-backed rows via REST first, so an empty
// ring overwrote them with a 1-row list on the first request (list blink,
// rows below vanished). Upstream seeds the ring from usageHistory once.
func TestTracker_RingSeededFromHistoryOnce(t *testing.T) {
	database, err := db.OpenDatabase(filepath.Join(t.TempDir(), "seed.sqlite"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	if err := dbtest.CreateTables(database); err != nil {
		t.Fatalf("create tables: %v", err)
	}

	repo := db.NewRepo(database)
	for i := 0; i < 3; i++ {
		if err := repo.InsertUsageHistory(
			"openai", "gpt-4o", "conn_1", "key_1", "/v1/chat/completions",
			100+i, 50+i, 0.01, "success", 0, "{}", `{"cached_tokens":7}`,
		); err != nil {
			t.Fatalf("insert history: %v", err)
		}
	}

	tracker := NewTracker()
	state := tracker.GetActiveState(repo)
	if len(state.RecentRequests) != 3 {
		t.Fatalf("expected ring seeded with 3 rows, got %d", len(state.RecentRequests))
	}
	if state.RecentRequests[0].Model != "gpt-4o" || state.RecentRequests[0].CachedTokens != 7 {
		t.Fatalf("unexpected seeded row: %+v", state.RecentRequests[0])
	}

	// A live push must win the head slot and not be duplicated by the seed.
	tracker.PushRecent(RecentRequest{
		Timestamp:        time.Now().UTC().Format(time.RFC3339),
		Model:            "claude-sonnet-4-6",
		Provider:         "anthropic",
		PromptTokens:     11,
		CompletionTokens: 22,
		Status:           "ok",
	}, repo)

	state = tracker.GetActiveState(repo)
	if len(state.RecentRequests) != 4 {
		t.Fatalf("expected 4 rows after push, got %d", len(state.RecentRequests))
	}
	if state.RecentRequests[0].Model != "claude-sonnet-4-6" {
		t.Fatalf("expected live push at head, got %+v", state.RecentRequests[0])
	}
}

func TestRecentFromHistoryRow_LegacyCachedTokens(t *testing.T) {
	for _, raw := range []string{
		`{"cache_read_input_tokens":21}`,
		`{"prompt_tokens_details":{"cached_tokens":22}}`,
		`{"input_tokens_details":{"cached_tokens":23}}`,
	} {
		row := recentFromHistoryRow(db.UsageHistoryRow{Tokens: raw})
		if row.CachedTokens == 0 {
			t.Fatalf("legacy cached tokens lost for %s", raw)
		}
	}
}

func TestTracker_ModelWithSpacesAndConcurrentDirectRequests(t *testing.T) {
	tracker := NewTracker()

	// Request 1: with spaces and parentheses in model name on connection
	tracker.TrackPending("Grok CLI (Grok Build)", "grok-cli", "conn_1", true, false)
	// Request 2: direct/no-auth request without connectionID
	tracker.TrackPending("space-bunny-free", "opencode", "", true, false)
	// Request 3: another model with spaces
	tracker.TrackPending("Claude 3.5 Sonnet", "anthropic", "conn_2", true, false)

	state := tracker.GetActiveState(nil)
	if len(state.ActiveRequests) != 3 {
		t.Fatalf("expected 3 active requests, got %d: %+v", len(state.ActiveRequests), state.ActiveRequests)
	}

	byProvider := make(map[string]ActiveRequest)
	for _, r := range state.ActiveRequests {
		byProvider[r.Provider] = r
	}

	grok, ok := byProvider["grok-cli"]
	if !ok || grok.Model != "Grok CLI (Grok Build)" {
		t.Errorf("grok-cli request incorrect: %+v", grok)
	}

	opencode, ok := byProvider["opencode"]
	if !ok || opencode.Model != "space-bunny-free" || opencode.Account != "Public / Direct" {
		t.Errorf("opencode direct request incorrect: %+v", opencode)
	}

	claude, ok := byProvider["anthropic"]
	if !ok || claude.Model != "Claude 3.5 Sonnet" {
		t.Errorf("anthropic request incorrect: %+v", claude)
	}

	// Clean up
	tracker.TrackPending("Grok CLI (Grok Build)", "grok-cli", "conn_1", false, false)
	tracker.TrackPending("space-bunny-free", "opencode", "", false, false)
	tracker.TrackPending("Claude 3.5 Sonnet", "anthropic", "conn_2", false, false)

	afterState := tracker.GetActiveState(nil)
	if len(afterState.ActiveRequests) != 0 {
		t.Errorf("expected 0 active requests after cleanup, got %d", len(afterState.ActiveRequests))
	}
}

func TestGetTracker_SingletonAndParseModelKey(t *testing.T) {
	tr := GetTracker()
	if tr == nil {
		t.Fatal("expected GetTracker to return non-nil instance")
	}

	m1, p1 := parseModelKey("gpt-4o (openai)")
	if m1 != "gpt-4o" || p1 != "openai" {
		t.Errorf("parseModelKey(gpt-4o (openai)) = %s, %s", m1, p1)
	}

	m2, p2 := parseModelKey("deepseek-chat")
	if m2 != "deepseek-chat" || p2 != "unknown" {
		t.Errorf("parseModelKey(deepseek-chat) = %s, %s", m2, p2)
	}
}
