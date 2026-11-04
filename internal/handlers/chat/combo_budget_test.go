package chat

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/db"
)

// budgetComboDB builds a repo whose settings carry one combo budget.
func budgetComboDB(t *testing.T, budgetJSON string) *db.Repo {
	t.Helper()
	database, cleanup := setupChatTestDB(t)
	t.Cleanup(cleanup)
	repo := db.NewRepo(database)
	if _, err := database.Exec(
		`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`,
		`{"comboStrategies":{`+budgetJSON+`}}`,
	); err != nil {
		t.Fatalf("insert settings: %v", err)
	}
	return repo
}

// TestComboBudgetGateAdmitsUntilCeiling pins the admission boundary: a combo
// with BudgetCalls=3 is admitted for three requests and withheld on the
// fourth, and the withheld request is still charged so the combo stays capped
// for the rest of the window instead of being re-admitted once per request.
func TestComboBudgetGateAdmitsUntilCeiling(t *testing.T) {
	repo := budgetComboDB(t, `"cheap":{"budgetCalls":3}`)
	h := &ChatHandler{Repo: repo}

	for i := 1; i <= 3; i++ {
		if h.comboBudgetExhausted("cheap") {
			t.Fatalf("request %d must be admitted: ceiling is 3", i)
		}
		h.recordComboBudgetCall("cheap")
	}
	if !h.comboBudgetExhausted("cheap") {
		t.Fatal("request 4 must be withheld: ceiling of 3 reached")
	}
	// A withheld request charges the combo too, so the gate cannot be beaten
	// by issuing refused requests.
	h.recordComboBudgetCall("cheap")
	if !h.comboBudgetExhausted("cheap") {
		t.Fatal("combo must stay withheld after a refused request is charged")
	}
}

// TestComboBudgetZeroMeansUnlimited pins the default: a combo with no budget
// field never gates, so every combo written before the feature is unaffected.
func TestComboBudgetZeroMeansUnlimited(t *testing.T) {
	repo := budgetComboDB(t, `"plain":{"stickyLimit":2}`)
	h := &ChatHandler{Repo: repo}
	for range 50 {
		if h.comboBudgetExhausted("plain") {
			t.Fatal("unbounded combo must never gate")
		}
		h.recordComboBudgetCall("plain")
	}
}

// TestComboBudgetUnknownComboDoesNotGate pins that a combo with no settings
// row at all is never gated: the gate reads configuration, it does not invent
// a ceiling for a name it has never seen.
func TestComboBudgetUnknownComboDoesNotGate(t *testing.T) {
	repo := budgetComboDB(t, `"cheap":{"budgetCalls":1}`)
	h := &ChatHandler{Repo: repo}
	for range 5 {
		if h.comboBudgetExhausted("never-configured") {
			t.Fatal("unconfigured combo must never gate")
		}
	}
}

// TestComboBudgetPerComboIsolation pins that one combo's spend does not
// consume another's ceiling.
func TestComboBudgetPerComboIsolation(t *testing.T) {
	repo := budgetComboDB(t, `"a":{"budgetCalls":2},"b":{"budgetCalls":2}`)
	h := &ChatHandler{Repo: repo}
	h.recordComboBudgetCall("a")
	h.recordComboBudgetCall("a")
	if !h.comboBudgetExhausted("a") {
		t.Fatal("combo a must be capped")
	}
	if h.comboBudgetExhausted("b") {
		t.Fatal("combo b must not inherit combo a's spend")
	}
}

// TestComboBudgetPruneUsesEachEntryOwnWindow pins that the prune pass expires a
// neighbour's counter against the window *it* was created under, not the
// recording combo's.
//
// This was a real bug: the prune compared every entry against the current
// combo's window, so a combo with a short window deleted a combo with a long
// window's live counter. The long combo then came back with a full ceiling,
// which is the operator's cap reopening — the one thing a budget exists to
// prevent. The pairing that makes it reachable is ordinary: a per-minute
// rate-limit cap next to a per-hour spend cap.
func TestComboBudgetPruneUsesEachEntryOwnWindow(t *testing.T) {
	repo := budgetComboDB(t, `"slow":{"budgetCalls":2,"budgetWindowMs":3600000},"fast":{"budgetCalls":2,"budgetWindowMs":5000}`)
	h := &ChatHandler{Repo: repo}

	// slow spends its whole ceiling inside its own window.
	h.recordComboBudgetCall("slow")
	h.recordComboBudgetCall("slow")
	if !h.comboBudgetExhausted("slow") {
		t.Fatal("setup: slow must be at its ceiling after 2 calls")
	}

	// Age slow's entry by 10s: expired inside fast's 5s window, still live
	// inside slow's own hour.
	h.comboBudgetMu.Lock()
	h.comboBudget["slow"].WindowStart = h.comboBudget["slow"].WindowStart.Add(-10 * time.Second)
	h.comboBudgetMu.Unlock()
	if !h.comboBudgetExhausted("slow") {
		t.Fatal("setup: slow must still be capped 10s into its hour")
	}

	// fast records once, which runs the prune pass.
	h.recordComboBudgetCall("fast")

	if !h.comboBudgetExhausted("slow") {
		t.Fatal("slow must keep its spend: it is 10s into its own hour, not expired")
	}
}

// TestComboBudgetPruneStillCollectsExpiredEntries pins that the prune still

// TestComboBudgetPruneStillCollectsExpiredEntries is the negative control for
// the fix above: the prune still does its job, so carrying each entry's own
// window cannot be mistaken for "prune does nothing".
//
// The prune skips the entry of the combo that is recording — that entry was
// just touched and is fresh by construction — so the expired entry has to
// belong to a third combo, and a *different* combo has to do the recording.
func TestComboBudgetPruneStillCollectsExpiredEntries(t *testing.T) {
	repo := budgetComboDB(t, `"short":{"budgetCalls":2,"budgetWindowMs":1},"long":{"budgetCalls":2,"budgetWindowMs":3600000}`)
	h := &ChatHandler{Repo: repo}
	// long spends its own ceiling, so the assertion is about the prune
	// preserving an already-capped neighbour rather than about counting.
	h.recordComboBudgetCall("long")
	h.recordComboBudgetCall("long")
	if !h.comboBudgetExhausted("long") {
		t.Fatal("setup: long must be at its ceiling before the prune runs")
	}
	h.recordComboBudgetCall("short")
	h.comboBudgetMu.Lock()
	// short is expired in its own 1ms window; long is fresh in its hour.
	h.comboBudget["short"].WindowStart = h.comboBudget["short"].WindowStart.Add(-2 * time.Second)
	h.comboBudgetMu.Unlock()
	// long records again, which runs the prune pass over the other entries.
	h.recordComboBudgetCall("long")
	h.comboBudgetMu.Lock()
	_, shortAlive := h.comboBudget["short"]
	h.comboBudgetMu.Unlock()
	if shortAlive {
		t.Fatal("short must be pruned: it is past its own window")
	}
	if !h.comboBudgetExhausted("long") {
		t.Fatal("long must keep its spend across the prune that removed short")
	}
}

// TestComboBudgetWindowRecycles pins that the counter restarts when the
// window elapses, which is what makes a restart-free gateway recover on its
// own instead of staying capped forever.
func TestComboBudgetWindowRecycles(t *testing.T) {
	repo := budgetComboDB(t, `"cheap":{"budgetCalls":1,"budgetWindowMs":1}`)
	h := &ChatHandler{Repo: repo}
	h.recordComboBudgetCall("cheap")
	if !h.comboBudgetExhausted("cheap") {
		t.Fatal("combo must be capped immediately after the ceiling")
	}
	// A 1 ms window elapses before the next call observes it.
	h.comboBudgetMu.Lock()
	h.comboBudget["cheap"].WindowStart = h.comboBudget["cheap"].WindowStart.Add(-2_000_000_000)
	h.comboBudgetMu.Unlock()
	if h.comboBudgetExhausted("cheap") {
		t.Fatal("expired window must admit the combo again")
	}
}

// TestSingleModelFromComboResolvesRegistryProvider pins the reviewer's finding:
// a combo whose first member is an ordinary registry provider must degrade into
// the same request the client would have made without the combo.
//
// The old implementation resolved through resolvePrefixProvider, which only
// knows provider *node* prefixes and returns nil for "openai/gpt-4o". Its
// fallback then stored the whole "provider/model" string in both Provider and
// Model, so a withheld request went upstream with the prefixed id and was
// answered 400. A registry provider as the first member is the common case.
func TestSingleModelFromComboResolvesRegistryProvider(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	got := h.singleModelFromCombo(nil, []string{"deepseek/deepseek-chat"})
	if got == nil {
		t.Fatal("expected a resolved single-model target")
	}
	if got.Provider != "deepseek" {
		t.Errorf("Provider = %q, want %q", got.Provider, "deepseek")
	}
	if got.Model != "deepseek-chat" {
		t.Errorf("Model = %q, want %q — the prefixed id must not reach upstream", got.Model, "deepseek-chat")
	}
	if strings.Contains(got.Model, "/") {
		t.Errorf("Model = %q must not carry the provider prefix", got.Model)
	}
	if got.ComboModels != nil || got.Strategy != "" || got.StickyLimit != 0 || got.JudgeModel != "" {
		t.Errorf("combo-only fields must be cleared, got %+v", got)
	}
}

// TestSingleModelFromComboNestedComboName pins that a single-name entry — a
// combo nested in another combo's list — is expanded to its first concrete
// model, which is what the client's own request for that name produced.
func TestSingleModelFromComboNestedComboName(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)

	innerModels, _ := json.Marshal([]string{"deepseek/deepseek-chat"})
	if _, err := database.Exec(
		`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES (?, ?, ?, ?, ?, ?)`,
		"inner", "inner-combo", "fallback", string(innerModels), "2026-07-19T00:00:00Z", "2026-07-19T00:00:00Z"); err != nil {
		t.Fatalf("insert inner combo: %v", err)
	}

	got := h.singleModelFromCombo(nil, []string{"inner-combo"})
	if got == nil {
		t.Fatal("expected the nested combo to resolve")
	}
	if got.Provider != "deepseek" || got.Model != "deepseek-chat" {
		t.Errorf("got %s/%s, want deepseek/deepseek-chat", got.Provider, got.Model)
	}
}

// TestSingleModelFromComboSplitsUnknownProvider pins the boundary: resolution
// does not require a live connection, so an unknown registry provider still
// resolves into its provider and unprefixed model. The degraded request must
// therefore carry the split, not the raw "provider/model" string — that is the
// whole bug this replaces.
func TestSingleModelFromComboSplitsUnknownProvider(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	h := NewChatHandler(db.NewRepo(database))

	got := h.singleModelFromCombo(nil, []string{"no-such-provider/no-such-model"})
	if got == nil {
		t.Fatal("expected a target")
	}
	if got.Provider != "no-such-provider" {
		t.Errorf("Provider = %q, want no-such-provider", got.Provider)
	}
	if got.Model != "no-such-model" {
		t.Errorf("Model = %q, want the unprefixed no-such-model", got.Model)
	}
}

// TestSingleModelFromComboFallsBackToComboInfo pins the empty-chain case: the
// combo's own ModelInfo is used, so a combo with no augmenting models still
// degrades into a single-model request rather than into nothing.
func TestSingleModelFromComboFallsBackToComboInfo(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	h := NewChatHandler(db.NewRepo(database))

	info := &ModelInfo{Provider: "deepseek", Model: "deepseek-chat", ConnectionID: "conn-1", ComboModels: []string{"a", "b"}}
	got := h.singleModelFromCombo(info, nil)
	if got.Provider != "deepseek" || got.Model != "deepseek-chat" {
		t.Errorf("got %s/%s, want the combo info's own target", got.Provider, got.Model)
	}
	if got.ConnectionID != "conn-1" {
		t.Errorf("ConnectionID = %q, want conn-1", got.ConnectionID)
	}
	if got.ComboModels != nil {
		t.Errorf("ComboModels must be cleared, got %v", got.ComboModels)
	}
}
