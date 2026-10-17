package usagetracker

import (
	json "encoding/json/v2"
	"path/filepath"
	"strings"
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

// A combo request asks for the combo and is served by a member, so the stream
// has to name both: the member in model/provider, the requested name and the
// combo in requestedModel/comboName. The pairing has to live exactly as long as
// the counter does — a stale one would keep calling a finished request a combo,
// and a plain request must never inherit someone else's combo name.
func TestTracker_RequestedModelPairing(t *testing.T) {
	tr := NewTracker()

	tr.TrackPendingRequested("PRO", "PRO", "deepseek-v4.1-flash", "tokenharbor", "conn_a", true, false)
	state := tr.GetActiveState(nil)
	if len(state.ActiveRequests) != 1 {
		t.Fatalf("expected 1 active request, got %d", len(state.ActiveRequests))
	}
	got := state.ActiveRequests[0]
	if got.Model != "deepseek-v4.1-flash" || got.Provider != "tokenharbor" {
		t.Errorf("model/provider must stay the member that serves it, got %s/%s", got.Model, got.Provider)
	}
	if got.RequestedModel != "PRO" || got.ComboName != "PRO" {
		t.Errorf("expected requestedModel/comboName PRO, got %q/%q", got.RequestedModel, got.ComboName)
	}

	// The payload the dashboard consumes carries both fields.
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal stream payload: %v", err)
	}
	for _, want := range []string{`"requestedModel":"PRO"`, `"comboName":"PRO"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("stream payload must expose %s, got %s", want, raw)
		}
	}

	// Two in-flight requests on the same member share one row and one pairing.
	tr.TrackPendingRequested("PRO", "PRO", "deepseek-v4.1-flash", "tokenharbor", "conn_a", true, false)
	state = tr.GetActiveState(nil)
	if len(state.ActiveRequests) != 1 || state.ActiveRequests[0].Count != 2 {
		t.Fatalf("expected one row with count 2, got %+v", state.ActiveRequests)
	}

	// Both finishing clears the pairing together with the counter.
	tr.TrackPendingRequested("PRO", "PRO", "deepseek-v4.1-flash", "tokenharbor", "conn_a", false, false)
	tr.TrackPendingRequested("PRO", "PRO", "deepseek-v4.1-flash", "tokenharbor", "conn_a", false, false)
	state = tr.GetActiveState(nil)
	if len(state.ActiveRequests) != 0 {
		t.Fatalf("expected the pairing to be gone with the counter, got %+v", state.ActiveRequests)
	}

	// A plain request carries the name it asked for and no combo.
	tr.TrackPendingRequested("gpt-6-sol", "", "gpt-6-sol", "cx", "conn_b", true, false)
	state = tr.GetActiveState(nil)
	if len(state.ActiveRequests) != 1 {
		t.Fatalf("expected 1 active request, got %d", len(state.ActiveRequests))
	}
	if state.ActiveRequests[0].ComboName != "" {
		t.Errorf("plain request must not carry a combo name, got %q", state.ActiveRequests[0].ComboName)
	}
	if state.ActiveRequests[0].RequestedModel != "gpt-6-sol" {
		t.Errorf("plain request must carry what it asked for, got %q", state.ActiveRequests[0].RequestedModel)
	}

	// TrackPending (media lanes) stays name-free so nothing is mislabelled.
	tr.TrackPending("whisper-1", "openai", "conn_c", true, false)
	state = tr.GetActiveState(nil)
	for _, r := range state.ActiveRequests {
		if r.ComboName != "" {
			t.Errorf("TrackPending must not invent a combo, got %q", r.ComboName)
		}
	}
}

// A completed request carries how long it took, so the flow card can badge the
// member that answered with its real time instead of a guess.
func TestTracker_RecentRequestCarriesLatency(t *testing.T) {
	tr := NewTracker()
	tr.PushRecent(RecentRequest{
		Timestamp:        time.Now().Format(time.RFC3339),
		Model:            "deepseek-v4.1-flash",
		Provider:         "tokenharbor",
		PromptTokens:     12,
		CompletionTokens: 4,
		Status:           "ok",
		RequestedModel:   "PRO",
		ComboName:        "PRO",
		LatencyMs:        1234,
	}, nil)

	state := tr.GetActiveState(nil)
	for _, r := range state.RecentRequests {
		if r.Model != "deepseek-v4.1-flash" {
			continue
		}
		if r.LatencyMs != 1234 {
			t.Errorf("expected latencyMs 1234, got %d", r.LatencyMs)
		}
		if r.ComboName != "PRO" {
			t.Errorf("expected the row to stay tagged with its combo, got %q", r.ComboName)
		}
		raw, err := json.Marshal(state)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if !strings.Contains(string(raw), `"latencyMs":1234`) {
			t.Errorf("stream payload must expose latencyMs, got %s", raw)
		}
		return
	}
	t.Fatalf("recent request missing from payload: %+v", state.RecentRequests)
}
