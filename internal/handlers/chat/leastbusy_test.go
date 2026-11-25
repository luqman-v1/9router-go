package chat

import (
	"testing"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/models"
)

// dbStrategy is the stored strategy shape the dispatch path reads.
func dbStrategy(name string) db.ProviderStrategy {
	return db.ProviderStrategy{RotateStrategy: name}
}

// newTestTracker returns a tracker on a clock the test drives.
func newTestTracker(now *time.Time) *LeastBusyTracker {
	t := NewLeastBusyTracker()
	t.now = func() time.Time { return *now }
	return t
}

func conns(ids ...string) []*models.ProviderConnection {
	out := make([]*models.ProviderConnection, len(ids))
	for i, id := range ids {
		out[i] = &models.ProviderConnection{ID: id, IsActive: 1}
	}
	return out
}

func connectionIDs(cs []*models.ProviderConnection) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.ID
	}
	return out
}

func TestLeastBusyPrefersIdleConnection(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tr := newTestTracker(&now)

	// "a" carries an in-flight request, "b" does not.
	tr.notePick("a")
	got := connectionIDs(tr.order(conns("a", "b")))

	if got[0] != "b" {
		t.Errorf("order = %v, want the idle connection first", got)
	}
}

func TestLeastBusyBreaksTiesWithoutPinningTheFirstRow(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tr := newTestTracker(&now)

	// Every connection is idle, so every draw is a valid spread. Over many
	// draws both ids must appear first at least once: a stable sort without the
	// random tie-break would return "a" every single time.
	first, second := 0, 0
	for i := 0; i < 200; i++ {
		switch connectionIDs(tr.order(conns("a", "b")))[0] {
		case "a":
			first++
		case "b":
			second++
		}
	}
	if first == 0 || second == 0 {
		t.Errorf("first place distribution a=%d b=%d: ties must not always pick the same row", first, second)
	}
}

func TestLeastBusyCountsRecentFailuresLowerThanInFlight(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tr := newTestTracker(&now)

	// "failed" reported one failure; "busy" has one request in flight. An
	// in-flight request outranks a past failure, so "failed" is tried first.
	tr.notePick("failed")
	tr.noteRelease("failed", true)
	tr.notePick("busy")

	got := connectionIDs(tr.order(conns("busy", "failed")))
	if got[0] != "failed" {
		t.Errorf("order = %v, want the recently-failed key before the busy one", got)
	}
}

func TestLeastBusyReleasesAndAgesOut(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tr := newTestTracker(&now)

	tr.notePick("a")
	tr.notePick("a")
	if got := tr.score("a", now); got != 2*inFlightWeight {
		t.Fatalf("score with two picks = %v, want %v", got, 2*inFlightWeight)
	}

	// A reported outcome frees one slot.
	tr.noteRelease("a", false)
	if got := tr.score("a", now); got != inFlightWeight {
		t.Errorf("score after one release = %v, want %v", got, inFlightWeight)
	}

	// A pick whose outcome never arrives stops counting once stale, so a lost
	// release cannot wedge a key forever.
	now = now.Add(inFlightStaleAfter + time.Second)
	if got := tr.score("a", now); got != 0 {
		t.Errorf("score after the stale window = %v, want 0", got)
	}
}

func TestLeastBusyRecentErrorExpires(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tr := newTestTracker(&now)

	tr.notePick("a")
	tr.noteRelease("a", true)
	if got := tr.score("a", now); got != recentErrorWeight {
		t.Fatalf("score right after a failure = %v, want %v", got, recentErrorWeight)
	}

	now = now.Add(recentErrorWindow + time.Second)
	if got := tr.score("a", now); got != 0 {
		t.Errorf("score after the error window = %v, want 0", got)
	}
}

func TestLeastBusyBoundsMemory(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tr := newTestTracker(&now)

	for i := 0; i < leastBusyMaxEntries+50; i++ {
		tr.notePick(string(rune('a'+i%26)) + itoa(i))
	}
	if got := len(tr.entries); got > leastBusyMaxEntries {
		t.Errorf("tracker holds %d entries, cap is %d", got, leastBusyMaxEntries)
	}

	// One key picked far more often than the per-key cap must not grow its
	// slice without bound either.
	for i := 0; i < maxInFlightPerKey*3; i++ {
		tr.notePick("hot")
	}
	if got := len(tr.entries["hot"].pickedAt); got > maxInFlightPerKey {
		t.Errorf("per-key picks = %d, cap is %d", got, maxInFlightPerKey)
	}
}

func TestLeastBusyIsAStrategy(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	prev := leastBusy
	leastBusy = newTestTracker(&now)
	t.Cleanup(func() { leastBusy = prev })

	h := &ChatHandler{}
	leastBusy.notePick("a")

	got := connectionIDs(h.applyConnectionStrategy(conns("a", "b"), dbStrategy("least-busy")))
	if got[0] != "b" {
		t.Errorf("least-busy strategy order = %v, want the idle connection first", got)
	}

	// The alias spelled without the hyphen is the same strategy, so an operator
	// who typed either gets load-aware routing instead of silently getting the
	// database order.
	got = connectionIDs(h.applyConnectionStrategy(conns("a", "b"), dbStrategy("leastbusy")))
	if got[0] != "b" {
		t.Errorf("leastbusy alias order = %v, want the idle connection first", got)
	}
}
