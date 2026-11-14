package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

// memLeaseStore is an in-memory LeaseStore for tests (two instances simulate
// two processes; they share nothing except via the shared store passed in).
type memLeaseStore struct {
	mu   sync.Mutex
	rows map[string]db.Lease
}

func newMemLeaseStore() *memLeaseStore {
	return &memLeaseStore{rows: map[string]db.Lease{}}
}

func (m *memLeaseStore) key(scope, key string) string { return scope + "\x00" + key }

func (m *memLeaseStore) ReadLease(scope, key string) (*db.Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.rows[m.key(scope, key)]
	if !ok || !l.ExpiresAt.After(time.Now().UTC()) {
		return nil, nil
	}
	cp := l
	return &cp, nil
}

func (m *memLeaseStore) AcquireLease(scope, key, value string, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	if l, ok := m.rows[m.key(scope, key)]; ok && l.ExpiresAt.After(now) {
		return false, nil
	}
	m.rows[m.key(scope, key)] = db.Lease{Scope: scope, Key: key, Value: value, ExpiresAt: now.Add(ttl)}
	return true, nil
}

func (m *memLeaseStore) ReleaseLease(scope, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l, ok := m.rows[m.key(scope, key)]; ok && l.Value == value {
		delete(m.rows, m.key(scope, key))
	}
	return nil
}

// Two "processes" (separate memory caches, shared lease store) racing one
// admission must converge on ONE instanceId: the loser follows the winner
// instead of POSTing its own claim (the hijack war this feature kills).
func TestFreebuffLease_TwoClaimersConverge(t *testing.T) {
	var posts int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != freebuffAdmissionPath {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		posts++
		n := posts
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"active","instanceId":"inst-` + string(rune('A'+n-1)) + `","expiresAt":"2099-01-01T00:00:00Z"}`))
	}))
	defer srv.Close()

	store := newMemLeaseStore()
	const token, model = "tok-converge", "m-converge"
	clearFreebuffSession(token, model)

	var wg sync.WaitGroup
	results := make([]*freebuffSession, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			s, err := requestFreebuffSessionWithStore(context.Background(), store, srv.Client(), srv.URL, token, model, nil)
			if err != nil {
				t.Errorf("claimer %d: %v", i, err)
				return
			}
			results[i] = s
		}()
	}
	wg.Wait()

	if results[0] == nil || results[1] == nil {
		t.Fatalf("both claimers must get a session: %+v", results)
	}
	if results[0].InstanceID != results[1].InstanceID {
		t.Fatalf("claimers diverged: %q vs %q (upstream would supersede one)",
			results[0].InstanceID, results[1].InstanceID)
	}
	mu.Lock()
	defer mu.Unlock()
	if posts != 1 {
		t.Fatalf("expected exactly 1 upstream POST, got %d", posts)
	}
}

// A lease written by a "sibling process" must be honored without any POST:
// getFreebuffSessionWithStore reads L2 and backfills L1.
func TestFreebuffLease_FollowerReadsNoPost(t *testing.T) {
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != freebuffAdmissionPath {
			http.NotFound(w, r)
			return
		}
		posts++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"active","instanceId":"inst-sib","expiresAt":"2099-01-01T00:00:00Z"}`))
	}))
	defer srv.Close()

	store := newMemLeaseStore()
	const token, model = "tok-follow", "m-follow"
	clearFreebuffSession(token, model)

	// Sibling admits first (separate memory, same store).
	sib, err := requestFreebuffSessionWithStore(context.Background(), store, srv.Client(), srv.URL, token, model, nil)
	if err != nil || sib.InstanceID != "inst-sib" {
		t.Fatalf("sibling admit failed: %+v %v", sib, err)
	}
	// Evict OUR memory only (sibling's memory is the same map in-test, so
	// clear + re-read proves the L2 path, not L1).
	clearFreebuffSession(token, model)

	got, ok := getFreebuffSessionWithStore(store, token, model)
	if !ok || got.InstanceID != "inst-sib" {
		t.Fatalf("expected follower to read inst-sib, got %+v %v", got, ok)
	}
	if posts != 1 {
		t.Fatalf("follower must not POST, total posts=%d", posts)
	}
}

// Compare-and-delete: dropping a stale lease must not delete a sibling's
// fresh row stored meanwhile.
func TestFreebuffLease_DropIsCompareAndDelete(t *testing.T) {
	store := newMemLeaseStore()
	const token, model = "tok-drop", "m-drop"

	if _, err := store.AcquireLease(freebuffLeaseScope, freebuffLeaseKey(token, model), "inst-old", time.Hour); err != nil {
		t.Fatal(err)
	}
	// Sibling re-admits and overwrites (takeover after expiry simulation:
	// release old, acquire new).
	if err := store.ReleaseLease(freebuffLeaseScope, freebuffLeaseKey(token, model), "inst-old"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireLease(freebuffLeaseScope, freebuffLeaseKey(token, model), "inst-new", time.Hour); err != nil {
		t.Fatal(err)
	}

	// Stale holder tries to drop with its old id: must be a no-op.
	dropFreebuffLease(store, token, model, "inst-old")
	got, err := store.ReadLease(freebuffLeaseScope, freebuffLeaseKey(token, model))
	if err != nil || got == nil || got.Value != "inst-new" {
		t.Fatalf("stale drop deleted fresh row: %+v %v", got, err)
	}
}

// requestFreebuffSession (nil store) keeps the old contract: POST + memory.
func TestFreebuffLease_NilStoreUnchanged(t *testing.T) {
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"active","instanceId":"inst-x","expiresAt":"2099-01-01T00:00:00Z"}`))
	}))
	defer srv.Close()

	const token, model = "tok-nil", "m-nil"
	clearFreebuffSession(token, model)
	sess, err := requestFreebuffSession(context.Background(), srv.Client(), srv.URL, token, model, nil)
	if err != nil || sess.InstanceID != "inst-x" {
		t.Fatalf("nil-store admit failed: %+v %v", sess, err)
	}
	if posts != 1 {
		t.Fatalf("expected 1 POST, got %d", posts)
	}
}

// Real SQLite backend (not the fake): two Repo handles on one file converge.
func TestFreebuffLease_SQLiteConverge(t *testing.T) {
	sqldb, err := db.OpenDatabase(t.TempDir() + "/leases.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer sqldb.Close()
	if err := db.EnsureUpstreamLeases(sqldb); err != nil {
		t.Fatal(err)
	}
	r1 := db.NewRepo(sqldb)
	r2 := db.NewRepo(sqldb)

	var posts int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		posts++
		n := posts
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"active","instanceId":"sql-` + string(rune('A'+n-1)) + `","expiresAt":"2099-01-01T00:00:00Z"}`))
	}))
	defer srv.Close()

	const token, model = "tok-sql", "m-sql"
	clearFreebuffSession(token, model)

	var wg sync.WaitGroup
	ids := make([]string, 2)
	stores := []LeaseStore{r1, r2}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			// Separate memory per "process" is the same map in-test; clear
			// between attempts is unnecessary: the claim mutex + lease
			// decide the winner regardless of L1.
			s, err := requestFreebuffSessionWithStore(context.Background(), stores[i], srv.Client(), srv.URL, token, model, nil)
			if err == nil && s != nil {
				ids[i] = s.InstanceID
			}
		}()
	}
	wg.Wait()
	if ids[0] == "" || ids[1] == "" {
		t.Fatalf("both must get a session: %v", ids)
	}
	if ids[0] != ids[1] {
		t.Fatalf("diverged on sqlite: %v", ids)
	}
	mu.Lock()
	defer mu.Unlock()
	if posts != 1 {
		t.Fatalf("expected 1 POST on sqlite, got %d", posts)
	}
}

func TestForwardFreebuff_UsesLeaseStore(t *testing.T) {
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case freebuffAdmissionPath:
			posts++
			_, _ = w.Write([]byte(`{"status":"active","instanceId":"inst-ls","expiresAt":"2099-01-01T00:00:00Z"}`))
		case freebuffRunPath:
			_, _ = w.Write([]byte(`{"runId":"run-ls"}`))
		default:
			if len(r.URL.Path) >= 17 && r.URL.Path[len(r.URL.Path)-17:] == "/chat/completions" {
				_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
			} else {
				http.NotFound(w, r)
			}
		}
	}))
	defer srv.Close()

	store := newMemLeaseStore()
	const token, model = "tok-fwls", "m-fwls"
	clearFreebuffSession(token, model)

	body := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`)
	mkReq := func() *Request {
		return &Request{
			Ctx:    context.Background(),
			Client: srv.Client(),
			Config: &providers.ProviderConfig{BaseURL: srv.URL + "/chat/completions"},
			APIKey: token,
			Body:   body,
			Leases: store,
		}
	}
	rec := httptest.NewRecorder()
	if err := ForwardFreebuff(rec, mkReq()); err != nil {
		t.Fatalf("first forward: %v", err)
	}
	// Second forward with a cold memory cache must follow the lease, not POST.
	clearFreebuffSession(token, model)
	rec2 := httptest.NewRecorder()
	if err := ForwardFreebuff(rec2, mkReq()); err != nil {
		t.Fatalf("second forward: %v", err)
	}
	if posts != 1 {
		t.Fatalf("expected 1 session POST across forwards, got %d", posts)
	}
}
