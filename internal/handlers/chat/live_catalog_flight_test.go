package chat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/models"
)

// TestLiveCatalogFlightSurvivesLeaderCancellation pins that the shared catalog
// fetch is not cancelled when the caller who started it disconnects.
//
// resolveLiveCatalog collapses N racing requests into one upstream call via
// singleflight, but the closure captured the leader's request context. When the
// leader's client hung up, context cancellation tore down the fetch for every
// waiter as well — and because an empty result caches nothing, the next
// dashboard poll repeated the whole call. The log showed the consequence: far
// more "context canceled" than "context deadline exceeded".
//
// The upstream here answers the first request only after the test releases it,
// and fails every later one, so the shared call must complete for the follower
// to see anything. That also pins that exactly one upstream call was made.
func TestLiveCatalogFlightSurvivesLeaderCancellation(t *testing.T) {
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
			w.Write([]byte(`{"data":[{"id":"m-one"}]}`))
			return
		}
		// Any second call means the flight was not shared: it would find
		// nothing, which is exactly the regression this test guards.
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	h := NewChatHandler(db.NewRepo(database))

	// A distinct connection id keeps the catalog cache key unique, so a result
	// cached by an earlier test cannot satisfy this one.
	conn := &models.ProviderConnection{
		ID:       "conn-flight-cancel",
		Provider: "openai-compatible-flight",
		IsActive: 1,
	}
	connData := &shared.ConnectionData{
		APIKey:               "sk-flight",
		ProviderSpecificData: map[string]any{"baseUrl": srv.URL},
	}
	const providerID = "openai-compatible-flight"

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	defer cancelLeader()
	leaderDone := make(chan []LiveModel, 1)
	go func() {
		leaderDone <- h.resolveLiveCatalog(leaderCtx, conn, connData, providerID)
	}()

	// The shared fetch is now in flight. Hang the leader's client up, which is
	// the ctx.Err() != nil state a disconnect leaves behind.
	<-entered
	cancelLeader()
	time.Sleep(50 * time.Millisecond)

	followerDone := make(chan []LiveModel, 1)
	go func() {
		followerDone <- h.resolveLiveCatalog(context.Background(), conn, connData, providerID)
	}()

	// Let the follower reach the in-flight call, then let the upstream answer.
	time.Sleep(50 * time.Millisecond)
	close(release)

	follower := <-followerDone
	<-leaderDone

	if len(follower) != 1 || follower[0].ID != "m-one" {
		t.Fatalf("follower got %v, want the leader's shared result [m-one]", idsOf(follower))
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("upstream calls = %d, want 1 — the follower should join the leader's flight", got)
	}
}

// TestResolveLiveCatalogStaleServesImmediately pins that an expired but
// previously good catalog is returned without waiting on a slow upstream.
// Without this, every /v1/models request after the 5-minute TTL takes another
// multi-second feed fetch, which is what made the endpoint exceed 10s.
func TestResolveLiveCatalogStaleServesImmediately(t *testing.T) {
	resetLiveCatalog(t)

	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"fresh-model","context_length":128000}]}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	h := NewChatHandler(db.NewRepo(database))
	h.Client = srv.Client()

	conn := &models.ProviderConnection{ID: "conn-stale-swr", Provider: "nous", IsActive: 1}
	connData := &shared.ConnectionData{APIKey: "sk-nous", BaseURL: srv.URL + "/v1/chat/completions"}
	key := liveCatalogKey("nous", conn, connData)
	// Seed a good catalog whose TTL has already passed.
	liveCatalogStoreModels(key, []LiveModel{{ID: "stale-model", ContextLength: 128000}})
	liveCatalogStore.mu.Lock()
	entry := liveCatalogStore.entries[key]
	entry.expiresAt = time.Now().Add(-time.Second)
	liveCatalogStore.entries[key] = entry
	liveCatalogStore.mu.Unlock()

	done := make(chan []LiveModel, 1)
	go func() {
		done <- h.resolveLiveCatalog(context.Background(), conn, connData, "nous")
	}()

	select {
	case got := <-done:
		if len(got) != 1 || got[0].ID != "stale-model" {
			t.Fatalf("stale catalog = %v, want the immediately served [stale-model]", idsOf(got))
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("stale catalog request blocked on the slow upstream refresh")
	}

	// Release the background refresh and wait for the cache to pick it up.
	unblock()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cached, ok := liveCatalogCached(key); ok && len(cached) == 1 && cached[0].ID == "fresh-model" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if cached, ok := liveCatalogCached(key); !ok || len(cached) != 1 || cached[0].ID != "fresh-model" {
		t.Errorf("refreshed cache = %v (ok=%v), want [fresh-model]", idsOf(cached), ok)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("upstream calls = %d, want exactly 1 background refresh", n)
	}
}
