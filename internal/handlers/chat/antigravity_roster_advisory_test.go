package chat

import (
	"testing"
	"time"
)

// The discovery roster is advisory. A tiered Antigravity account answers 200
// for gemini-4-carbon / gemini-4-argon while fetchAvailableModels omits them,
// so blocking on absence alone parked a working model until the process
// restarted. Only a real upstream 404 parks the pair.
func TestAntigravityRosterMiss_IsAdvisory(t *testing.T) {
	ClearAntigravityQuotaCache()
	conn := "ag-test-advisory"
	agRosterMu.Lock()
	agRosterCache[conn] = map[string]struct{}{"gemini-3.8-flash-tiered": {}}
	agRosterMu.Unlock()

	if !IsAntigravityModelServed(conn, "gemini-4-carbon") {
		// Sanity: the roster really does omit it; that is the input to the rule.
		t.Log("roster omits gemini-4-carbon, as expected")
	}
	if IsAntigravityModelBlocked(conn, "gemini-4-carbon") {
		t.Fatal("a model missing from the roster must still be attempted")
	}
	if IsAntigravityModelBlocked(conn, "gemini-4-argon") {
		t.Fatal("every omitted family must be attempted, not just carbon")
	}
}

// A 404 from the upstream is the one verdict worth caching: after it the pair
// stops costing a request.
func TestAntigravityModelNotFound_ParksThePair(t *testing.T) {
	ClearAntigravityQuotaCache()
	conn := "ag-test-404"
	agRosterMu.Lock()
	agRosterCache[conn] = map[string]struct{}{"gemini-3.8-flash-tiered": {}}
	agRosterMu.Unlock()

	NoteAntigravityModelNotFound(conn, "gemini-4-hydro")
	if !IsAntigravityModelBlocked(conn, "gemini-4-hydro") {
		t.Fatal("a model the upstream 404'd must be parked")
	}
	if IsAntigravityModelBlocked(conn, "gemini-4-carbon") {
		t.Error("the park is per model, not per connection")
	}
	// A served model stays served even after another one 404s.
	if IsAntigravityModelBlocked(conn, "gemini-3.8-flash-tiered") {
		t.Error("a roster member must not be affected by another model's 404")
	}
}

func TestAntigravityModelNotFound_ExpiresAndClears(t *testing.T) {
	ClearAntigravityQuotaCache()
	conn := "ag-test-expiry"
	agRosterMu.Lock()
	agRosterCache[conn] = map[string]struct{}{}
	agRosterMu.Unlock()

	agRosterMissMu.Lock()
	agRosterMissBlocked[conn+"|gemini-4-hydro"] = time.Now().UTC().Add(-time.Minute)
	agRosterMissMu.Unlock()
	if IsAntigravityModelBlocked(conn, "gemini-4-hydro") {
		t.Error("an expired park must not block")
	}

	NoteAntigravityModelNotFound(conn, "gemini-4-hydro")
	ClearAntigravityQuotaCache()
	if IsAntigravityModelBlocked(conn, "gemini-4-hydro") {
		t.Error("ClearAntigravityQuotaCache must drop the parks too")
	}
}
