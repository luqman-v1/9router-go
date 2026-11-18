package chat

import (
	"sync"
	"sync/atomic"
	"testing"

	"9router/proxy/internal/db"
)

// The round-robin cursor must hand every concurrent caller a different pool.
//
// The first version of the walk read the cursor with Load() and wrote it back
// with Store() later in the function. That is a read-then-write, not an atomic
// claim: two requests arriving together both Load the same position, both scan
// from it, and both return the same pool — so under real concurrency the
// rotation stops rotating exactly when it matters most. Upstream's single-pool
// picker never had the bug because it used Add(1)-1; the walk reintroduced it
// when it grew a scan loop.
//
// This pins the property the fix restores: N concurrent calls over a pool of N
// egresses must return N distinct egresses.
//
// The provider is one without cooldown support on purpose. That makes the call
// take the lock-free second pass, so the claim and the advance are the only
// synchronization between callers — with a cooldown-capable provider the
// per-candidate mutex inside poolCoolingDown serialises callers enough that the
// bug almost never reproduces.
//
// What this test guarantees, and what it does not: an atomic claim returns
// distinct pools on every run, so a correct implementation never flakes here.
// A non-atomic one fails only when two callers actually overlap, which depends
// on scheduling — measured at 10/10 failing runs under contention on a 10-core
// box, but 0/5 in a quieter run. Passing therefore does not prove the claim is
// atomic; failing does prove it is not.
func TestPoolRotationCursorIsClaimedAtomically(t *testing.T) {
	const pools = 128

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	dropSeededConn(t, database)

	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS proxyPools (
		id TEXT PRIMARY KEY,
		isActive INTEGER DEFAULT 1,
		testStatus TEXT,
		data TEXT NOT NULL,
		createdAt TEXT NOT NULL,
		updatedAt TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("create proxyPools: %v", err)
	}
	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS settings (id INTEGER PRIMARY KEY, data TEXT);`); err != nil {
		t.Fatalf("create settings: %v", err)
	}
	for i := range pools {
		if _, err := database.Exec(`INSERT INTO proxyPools (id, isActive, testStatus, data, createdAt, updatedAt)
			VALUES (?, 1, 'unknown', ?, '2026-11-18T00:00:00Z', '2026-11-18T00:00:00Z')`,
			"atomic-pool-"+itoa(i),
			`{"name":"Atomic `+itoa(i)+`","urls":["socks5://u:p@10.0.0.`+itoa(i)+`:1080"],"type":"socks5"}`,
		); err != nil {
			t.Fatalf("insert pool %d: %v", i, err)
		}
	}

	h := &ChatHandler{Repo: db.NewRepo(database)}

	// Reset so the first call starts at a known position.
	poolRotationCursors.Delete("gemini")
	t.Cleanup(func() { poolRotationCursors.Delete("gemini") })

	// Release all callers at once: staggering them would let the cursor advance
	// between calls and hide a read-then-write entirely.
	var (
		start  = make(chan struct{})
		wg     sync.WaitGroup
		mu     sync.Mutex
		picked = make([]string, 0, pools)
	)
	for range pools {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			id, err := h.rotatedActiveProxyPoolWithin("gemini", "round-robin", nil)
			if err != nil {
				return
			}
			mu.Lock()
			picked = append(picked, id)
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()

	if len(picked) != pools {
		t.Fatalf("got %d pool picks, want %d — a walk returned an error", len(picked), pools)
	}

	seen := make(map[string]int, pools)
	for _, id := range picked {
		seen[id]++
	}
	if len(seen) != pools {
		dupes := make(map[string]int)
		for id, n := range seen {
			if n > 1 {
				dupes[id] = n
			}
		}
		t.Fatalf("%d concurrent calls over %d pools returned only %d distinct ones; "+
			"repeated picks: %v — the cursor is being read and written non-atomically",
			pools, pools, len(seen), dupes)
	}
}

// advancePoolCursorPast only ever moves forward.
//
// A slower caller whose scan landed earlier must not pull the cursor back over
// positions a faster caller has already consumed: doing so would hand the same
// pool out twice, which is the failure the atomic claim exists to prevent. This
// is the deterministic half of that contract — unlike the concurrency test
// above, it cannot be satisfied by scheduling luck.
func TestAdvancePoolCursorPastNeverMovesBackwards(t *testing.T) {
	var cursor atomic.Uint64

	tests := []struct {
		name   string
		from   uint64
		target uint64
		want   uint64
	}{
		{name: "advances from zero", from: 0, target: 1, want: 1},
		{name: "advances past a scanned run", from: 1, target: 4, want: 4},
		{name: "ignores an equal target", from: 4, target: 4, want: 4},
		{name: "refuses to move backwards", from: 4, target: 2, want: 4},
		{name: "refuses to rewind to zero", from: 9, target: 0, want: 9},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cursor.Store(tt.from)
			advancePoolCursorPast(&cursor, tt.target)
			if got := cursor.Load(); got != tt.want {
				t.Errorf("cursor = %d, want %d", got, tt.want)
			}
		})
	}
}
