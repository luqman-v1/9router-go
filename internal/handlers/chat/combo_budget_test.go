package chat

import (
	"testing"

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
