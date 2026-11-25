package chat

import (
	"math/rand/v2"
	"sort"
	"sync"
	"time"

	"9router/proxy/internal/models"
)

// Least-busy connection selection: prefer the eligible connection carrying the
// fewest outstanding requests, so a multi-key provider spreads an agent's load
// across its keys instead of piling every turn onto the first one.
//
// The signal is cheap and bounded, the upstream lesson applied to the Go
// dispatch path: in-flight picks counted in memory at selection time plus recent
// failures in a short window. No timer, no polling loop, no dependency, no extra
// database read — the forward path notes every pick and every outcome, so a long
// streaming reply counts as load for the whole turn it occupies the key.
//
// Single-process assumption, like the sticky and affinity state beside it: two
// processes serving one provider would each keep their own counts.
//
// Stale safety: an outcome that never reports (a request that panics out, a
// context cancelled before the defer) would leave a phantom in-flight entry, so
// entries age out lazily on read after inFlightStaleAfter instead of wedging a
// key forever. Memory is capped at leastBusyMaxEntries with per-key caps on the
// stored timestamps and idle entries dropped outright.

const (
	// inFlightWeight and recentErrorWeight shape the score. One in-flight
	// request is a real, current cost; one recent failure is a warning about a
	// key that may fail again, not proof that it is busy, so it weighs less.
	inFlightWeight    = 1.0
	recentErrorWeight = 0.5

	// recentErrorWindow is how long a reported failure keeps weighing on a key.
	recentErrorWindow = 30 * time.Second

	// inFlightStaleAfter bounds how long one pick counts as load without an
	// outcome. A turn longer than this is possible; counting it forever is not,
	// because nothing would ever release it if the outcome was lost.
	inFlightStaleAfter = 10 * time.Minute

	// leastBusyMaxEntries caps the map. A provider fleet can be large — 2.4k
	// keys — and the map must not grow with every connection ever seen.
	leastBusyMaxEntries = 4096

	// Per-key caps on the stored slices, so one hot key cannot grow the map's
	// memory without bound.
	maxInFlightPerKey = 64
	maxErrorsPerKey   = 32
)

// leastBusyEntry is one key's load. pickedAt holds one timestamp per
// outstanding request; errors holds one per failure inside the window.
type leastBusyEntry struct {
	pickedAt  []time.Time
	errors    []time.Time
	touchedAt time.Time
}

// LeastBusyTracker is the process-wide load table. Its zero value is unusable;
// use the package instance.
type LeastBusyTracker struct {
	mu      sync.Mutex
	entries map[string]*leastBusyEntry
	now     func() time.Time // swapped by tests
}

// NewLeastBusyTracker returns an empty tracker on the wall clock.
func NewLeastBusyTracker() *LeastBusyTracker {
	return &LeastBusyTracker{entries: make(map[string]*leastBusyEntry), now: time.Now}
}

// leastBusy is the shared instance the dispatch path notes against.
var leastBusy = NewLeastBusyTracker()

// notePick records that a request is now in flight on one connection.
func (t *LeastBusyTracker) notePick(id string) {
	if id == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	entry := t.entryFor(id, now)
	if len(entry.pickedAt) >= maxInFlightPerKey {
		// A key already at the cap is loaded beyond what the score can express;
		// dropping the oldest keeps the count from growing while leaving the
		// key visibly busiest.
		entry.pickedAt = entry.pickedAt[1:]
	}
	entry.pickedAt = append(entry.pickedAt, now)
}

// noteRelease records that an attempt on one connection finished. A failure is
// remembered for recentErrorWindow, so a key that just refused a request is
// deprioritized behind one that has been serving.
func (t *LeastBusyTracker) noteRelease(id string, failed bool) {
	if id == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	entry := t.entryFor(id, now)
	// Release the OLDEST outstanding pick: picks are appended in order, and the
	// request that just finished is one of them — which one does not matter for
	// a count, and the oldest is the one closest to aging out anyway.
	if len(entry.pickedAt) > 0 {
		entry.pickedAt = entry.pickedAt[1:]
	}
	if failed {
		if len(entry.errors) >= maxErrorsPerKey {
			entry.errors = entry.errors[1:]
		}
		entry.errors = append(entry.errors, now)
	}
	if len(entry.pickedAt) == 0 && len(entry.errors) == 0 {
		delete(t.entries, id)
	}
}

// score is a key's current load. Lower is better.
func (t *LeastBusyTracker) score(id string, now time.Time) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	entry, ok := t.entries[id]
	if !ok {
		return 0
	}
	t.prune(entry, now, id)
	return float64(len(entry.pickedAt))*inFlightWeight + float64(len(entry.errors))*recentErrorWeight
}

// order returns the connections sorted least-busy first, breaking ties at
// random so a burst of idle keys still spreads instead of always taking the
// first row. It never mutates load state; the forward path notes the pick.
func (t *LeastBusyTracker) order(conns []*models.ProviderConnection) []*models.ProviderConnection {
	if len(conns) <= 1 {
		return conns
	}
	now := t.now()
	type scored struct {
		conn  *models.ProviderConnection
		score float64
		tie   int
	}
	ranked := make([]scored, 0, len(conns))
	for _, c := range conns {
		if c == nil {
			continue
		}
		ranked = append(ranked, scored{conn: c, score: t.score(c.ID, now), tie: rand.IntN(len(conns))})
	}
	// Least busy first; exact ties fall back to a random draw so a burst of
	// equally idle keys still spreads across them.
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score < ranked[j].score
		}
		return ranked[i].tie < ranked[j].tie
	})
	out := make([]*models.ProviderConnection, len(ranked))
	for i, r := range ranked {
		out[i] = r.conn
	}
	return out
}

// entryFor returns the entry for id, creating it and evicting an idle one when
// the map is at its cap. Caller holds the lock.
func (t *LeastBusyTracker) entryFor(id string, now time.Time) *leastBusyEntry {
	if entry, ok := t.entries[id]; ok {
		entry.touchedAt = now
		return entry
	}
	if len(t.entries) >= leastBusyMaxEntries {
		t.evictOldestIdle(id)
	}
	entry := &leastBusyEntry{touchedAt: now}
	t.entries[id] = entry
	return entry
}

// evictOldestIdle drops the least recently touched idle entry, falling back to
// the least recently touched entry of any kind when every one is busy.
// Caller holds the lock.
func (t *LeastBusyTracker) evictOldestIdle(newID string) {
	var idleID, anyID string
	var idleAt, anyAt time.Time
	for id, entry := range t.entries {
		if id == newID {
			continue
		}
		if idleID == "" || entry.touchedAt.Before(idleAt) {
			if len(entry.pickedAt) == 0 && len(entry.errors) == 0 {
				idleID, idleAt = id, entry.touchedAt
			}
		}
		if anyID == "" || entry.touchedAt.Before(anyAt) {
			anyID, anyAt = id, entry.touchedAt
		}
	}
	if idleID != "" {
		delete(t.entries, idleID)
		return
	}
	if anyID != "" {
		delete(t.entries, anyID)
	}
}

// prune drops timestamps outside their windows and deletes the entry when
// nothing is left. Caller holds the lock.
func (t *LeastBusyTracker) prune(entry *leastBusyEntry, now time.Time, id string) {
	inFlightCutoff := now.Add(-inFlightStaleAfter)
	for len(entry.pickedAt) > 0 && !entry.pickedAt[0].After(inFlightCutoff) {
		entry.pickedAt = entry.pickedAt[1:]
	}
	errorCutoff := now.Add(-recentErrorWindow)
	for len(entry.errors) > 0 && !entry.errors[0].After(errorCutoff) {
		entry.errors = entry.errors[1:]
	}
	if len(entry.pickedAt) == 0 && len(entry.errors) == 0 {
		delete(t.entries, id)
	}
}
