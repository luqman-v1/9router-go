package chat

import (
	"strconv"
	"strings"
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

// The model half of the park key is caller-supplied, so the table needs a hard
// ceiling: a client that names a new model on every request must not be able to
// grow it. A full table refuses the park, never the request.
func TestNoteAntigravityModelNotFound_TableIsBounded(t *testing.T) {
	ClearAntigravityQuotaCache()
	conn := "ag-test-bounded"
	now := time.Now().UTC()

	// Expired rows make room: the insert sweeps instead of refusing.
	agRosterMissMu.Lock()
	for i := 0; i < agRosterMissMaxEntries; i++ {
		agRosterMissBlocked[conn+"|expired-"+strconv.Itoa(i)] = now.Add(-time.Minute)
	}
	agRosterMissMu.Unlock()
	NoteAntigravityModelNotFound(conn, "fresh-model")
	agRosterMissMu.RLock()
	afterSweep := len(agRosterMissBlocked)
	_, parked := agRosterMissBlocked[conn+"|fresh-model"]
	agRosterMissMu.RUnlock()
	if !parked {
		t.Fatal("an expired table must sweep and then park the new pair")
	}
	if afterSweep > agRosterMissMaxEntries {
		t.Fatalf("sweep left %d rows, ceiling is %d", afterSweep, agRosterMissMaxEntries)
	}

	// Live rows do not: the table stays at its ceiling and the park is skipped.
	ClearAntigravityQuotaCache()
	agRosterMissMu.Lock()
	for i := 0; i < agRosterMissMaxEntries; i++ {
		agRosterMissBlocked[conn+"|live-"+strconv.Itoa(i)] = now.Add(time.Hour)
	}
	agRosterMissMu.Unlock()
	NoteAntigravityModelNotFound(conn, "one-too-many")
	agRosterMissMu.RLock()
	size := len(agRosterMissBlocked)
	_, over := agRosterMissBlocked[conn+"|one-too-many"]
	agRosterMissMu.RUnlock()
	if over {
		t.Error("a full table must not accept another pair")
	}
	if size > agRosterMissMaxEntries {
		t.Errorf("table grew past its ceiling: %d", size)
	}

	// A pair already in the table is refreshed even at the ceiling: refusing
	// that would unpark a model mid-window.
	NoteAntigravityModelNotFound(conn, "live-0")
	agRosterMissMu.RLock()
	_, kept := agRosterMissBlocked[conn+"|live-0"]
	agRosterMissMu.RUnlock()
	if !kept {
		t.Error("an existing park must still refresh at the ceiling")
	}

	// An absurd id is not worth a row, and must not become one.
	NoteAntigravityModelNotFound(conn, strings.Repeat("x", agRosterMissMaxModelLen+1))
	agRosterMissMu.RLock()
	long := len(agRosterMissBlocked)
	agRosterMissMu.RUnlock()
	if long != size {
		t.Errorf("an over-length model must not park: table grew to %d", long)
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
