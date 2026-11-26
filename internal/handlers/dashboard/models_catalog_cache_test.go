package dashboard

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// resetModelsCatalog swaps in a fresh cache so each test starts empty.
func resetModelsCatalog(t *testing.T) {
	t.Helper()
	prev := liveModelsCatalog
	liveModelsCatalog = &modelsCatalog{entries: make(map[string]modelsCatalogEntry), gens: make(map[string]uint64)}
	t.Cleanup(func() { liveModelsCatalog = prev })
}

func TestModelsCatalogCachesSuccessfulBody(t *testing.T) {
	resetModelsCatalog(t)
	var calls atomic.Int32

	produce := func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"models":[{"id":"a"}]}`))
	}

	for i := range 3 {
		rec := httptest.NewRecorder()
		liveModelsCatalog.serve(rec, httptest.NewRequest(http.MethodGet, "/", nil), "conn-1", produce)
		if rec.Code != http.StatusOK {
			t.Fatalf("call %d: status = %d, want 200", i, rec.Code)
		}
		if got := rec.Body.String(); got != `{"models":[{"id":"a"}]}` {
			t.Fatalf("call %d: body = %q", i, got)
		}
	}

	if got := calls.Load(); got != 1 {
		t.Errorf("upstream called %d times, want 1 (later opens must be cache hits)", got)
	}
}

func TestModelsCatalogDoesNotCacheErrors(t *testing.T) {
	resetModelsCatalog(t)
	var calls atomic.Int32

	produce := func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"bad key"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"models":[]}`))
	}

	rec := httptest.NewRecorder()
	liveModelsCatalog.serve(rec, httptest.NewRequest(http.MethodGet, "/", nil), "conn-2", produce)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("first call status = %d, want 401", rec.Code)
	}

	// A transient failure must not be replayed: the second call reaches upstream.
	rec2 := httptest.NewRecorder()
	liveModelsCatalog.serve(rec2, httptest.NewRequest(http.MethodGet, "/", nil), "conn-2", produce)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second call status = %d, want 200 (error must not be cached)", rec2.Code)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("upstream called %d times, want 2", got)
	}
}

func TestModelsCatalogCollapsesConcurrentMisses(t *testing.T) {
	resetModelsCatalog(t)
	var calls atomic.Int32
	release := make(chan struct{})

	produce := func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		<-release // hold the flight open so the others queue behind it
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"models":[]}`))
	}

	const callers = 8
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			liveModelsCatalog.serve(rec, httptest.NewRequest(http.MethodGet, "/", nil), "conn-3", produce)
			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200", rec.Code)
			}
		}()
	}

	// Give every caller time to join the flight before letting it complete.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Errorf("upstream called %d times for %d concurrent callers, want 1", got, callers)
	}
}

func TestModelsCatalogInvalidateForcesRefetch(t *testing.T) {
	resetModelsCatalog(t)
	var calls atomic.Int32

	produce := func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"models":[]}`))
	}

	rec := httptest.NewRecorder()
	liveModelsCatalog.serve(rec, httptest.NewRequest(http.MethodGet, "/", nil), "conn-4", produce)
	liveModelsCatalog.invalidate("conn-4")
	rec2 := httptest.NewRecorder()
	liveModelsCatalog.serve(rec2, httptest.NewRequest(http.MethodGet, "/", nil), "conn-4", produce)

	if got := calls.Load(); got != 2 {
		t.Errorf("upstream called %d times, want 2 (invalidate must force a refetch)", got)
	}
}

func TestModelsCatalogEntryExpires(t *testing.T) {
	resetModelsCatalog(t)
	liveModelsCatalog.store("conn-5", []byte(`{"models":[]}`))

	// Expire the entry by hand rather than sleeping a minute.
	liveModelsCatalog.mu.Lock()
	entry := liveModelsCatalog.entries["conn-5"]
	entry.expiresAt = time.Now().Add(-time.Second)
	liveModelsCatalog.entries["conn-5"] = entry
	liveModelsCatalog.mu.Unlock()

	var calls atomic.Int32
	produce := func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"models":[]}`))
	}
	rec := httptest.NewRecorder()
	liveModelsCatalog.serve(rec, httptest.NewRequest(http.MethodGet, "/", nil), "conn-5", produce)

	if got := calls.Load(); got != 1 {
		t.Errorf("upstream called %d times, want 1 (an expired entry must be refetched)", got)
	}
}

func TestModelsCatalogEvictsWhenFull(t *testing.T) {
	resetModelsCatalog(t)
	for i := range modelsCatalogMaxEntries + 5 {
		liveModelsCatalog.store(string(rune('a'+i%26))+string(rune('0'+i/26)), []byte(`{}`))
	}
	liveModelsCatalog.mu.Lock()
	size := len(liveModelsCatalog.entries)
	liveModelsCatalog.mu.Unlock()
	if size > modelsCatalogMaxEntries {
		t.Errorf("cache holds %d entries, want at most %d", size, modelsCatalogMaxEntries)
	}
}

func TestModelsCatalogWriterCapturesStatusAndBody(t *testing.T) {
	cw := &captureWriter{header: make(http.Header)}
	cw.Header().Set("X-Test", "1")
	cw.WriteHeader(http.StatusTeapot)
	_, _ = cw.Write([]byte("hi"))

	if cw.status != http.StatusTeapot {
		t.Errorf("status = %d, want 418", cw.status)
	}
	if got := string(cw.body.Bytes()); got != "hi" {
		t.Errorf("body = %q, want %q", got, "hi")
	}
}

func TestModelsCatalogWriterDefaultsTo200(t *testing.T) {
	cw := &captureWriter{header: make(http.Header)}
	_, _ = cw.Write([]byte("x"))
	if cw.status != http.StatusOK {
		t.Errorf("status = %d, want 200 when Write precedes WriteHeader", cw.status)
	}
}

// A cached catalogue belongs to the configuration it was fetched with. Editing
// or deleting the connection must drop it, or the dashboard keeps showing the
// previous node's models for the full TTL after the operator changed baseUrl.
func TestModelsCatalogInvalidatedByConnectionEditAndDelete(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo) // starts with a cold cache

	const connID = "conn-cache-invalidate"
	if err := repo.CreateProviderConnectionFull(context.Background(), connID, "openai-compatible-chat-abc", "apikey", "Node", nil,
		`{"apiKey":"sk-x","providerSpecificData":{"baseUrl":"https://api.example.com/v1"}}`); err != nil {
		t.Fatalf("create connection: %v", err)
	}

	liveModelsCatalog.store(connID, []byte(`{"models":[{"id":"stale"}]}`))
	if !modelsCatalogHas(connID) {
		t.Fatal("precondition: cache entry should be present")
	}

	req := httptest.NewRequest(http.MethodPut, "/api/connections/"+connID, bytes.NewReader([]byte(`{"name":"Renamed"}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if modelsCatalogHas(connID) {
		t.Error("edit left a stale catalogue cached")
	}

	liveModelsCatalog.store(connID, []byte(`{"models":[{"id":"stale"}]}`))
	delReq := httptest.NewRequest(http.MethodDelete, "/api/connections/"+connID, nil)
	delRec := httptest.NewRecorder()
	router.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200: %s", delRec.Code, delRec.Body.String())
	}
	if modelsCatalogHas(connID) {
		t.Error("delete left a cached catalogue for a removed connection")
	}
}

// modelsCatalogHas reports whether a connection has a live cache entry.
func modelsCatalogHas(connID string) bool {
	liveModelsCatalog.mu.Lock()
	defer liveModelsCatalog.mu.Unlock()
	_, ok := liveModelsCatalog.entries[connID]
	return ok
}

// An invalidation that lands while a fetch is in flight must not be undone by
// that fetch storing what it read. Before the generation counter, the sync path
// cleared the entry, the in-flight fetch completed and wrote the pre-sync body
// back, and the very next request was answered from the cache the sync existed
// to bypass.
func TestModelsCatalogInvalidateDuringFlightDoesNotRestoreStaleBody(t *testing.T) {
	resetModelsCatalog(t)
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	first := true

	produce := func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		if first {
			first = false
			close(started)
			<-release
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"models":[{"id":"pre-sync"}]}`))
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		rec := httptest.NewRecorder()
		liveModelsCatalog.serve(rec, httptest.NewRequest(http.MethodGet, "/", nil), "conn-race", produce)
	}()

	<-started
	liveModelsCatalog.invalidate("conn-race")
	close(release)
	<-done

	if entry, ok := liveModelsCatalog.entries["conn-race"]; ok {
		t.Fatalf("stale body restored after invalidate: %q", entry.body)
	}

	// The next request must reach the upstream, not the body the sync cleared.
	rec := httptest.NewRecorder()
	liveModelsCatalog.serve(rec, httptest.NewRequest(http.MethodGet, "/", nil), "conn-race", produce)
	if got := calls.Load(); got != 2 {
		t.Errorf("upstream called %d times, want 2 (invalidate must survive the in-flight fetch)", got)
	}
}
