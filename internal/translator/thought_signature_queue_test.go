package translator

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"9router/proxy/internal/db"
)

// The discovery doc (§3.2–§3.4) requires the persistent tier to write through a
// bounded queue drained by one goroutine, so a burst of tool calls never blocks
// the request that produced the signature. The tests here pin that against the
// real store, not a parallel implementation of it.

// slowPersister records every save and can be told to stall, so a test can fill
// the queue deterministically instead of racing the writer.
type slowPersister struct {
	inner   *db.Repo
	release chan struct{}
	once    sync.Once
	saves   atomic.Int64
	mu      sync.Mutex
	keys    []string
}

func (s *slowPersister) SaveThoughtSignature(ctx context.Context, key, signature, family string, expiresAt time.Time) error {
	<-s.release
	s.saves.Add(1)
	s.mu.Lock()
	s.keys = append(s.keys, key)
	s.mu.Unlock()
	return s.inner.SaveThoughtSignature(ctx, key, signature, family, expiresAt)
}

func (s *slowPersister) GetThoughtSignature(ctx context.Context, key string) (*db.ThoughtSignatureRow, error) {
	return s.inner.GetThoughtSignature(ctx, key)
}

func (s *slowPersister) LoadThoughtSignatures(ctx context.Context, limit int) ([]db.ThoughtSignatureRow, error) {
	return s.inner.LoadThoughtSignatures(ctx, limit)
}

func (s *slowPersister) PruneThoughtSignatures(ctx context.Context, now time.Time) (int64, error) {
	return s.inner.PruneThoughtSignatures(ctx, now)
}

func (s *slowPersister) TrimThoughtSignatures(ctx context.Context, keep int) (int64, error) {
	return s.inner.TrimThoughtSignatures(ctx, keep)
}

// unblock releases the stalled writer. Idempotent: the failure path releases
// it before the cleanup does.
func (s *slowPersister) unblock() { s.once.Do(func() { close(s.release) }) }

// TestThoughtSignatureStore_QueueSaturationFailOpen is the §3.3 contract: with
// the writer stalled, a burst of writes must return promptly, keep every entry
// in L1, and count the drops instead of blocking or panicking.
func TestThoughtSignatureStore_QueueSaturationFailOpen(t *testing.T) {
	repo := openTestSignatureRepo(t)
	p := &slowPersister{inner: repo, release: make(chan struct{})}
	t.Cleanup(func() { SetThoughtSignaturePersistence(nil) })
	SetThoughtSignaturePersistence(p)
	t.Cleanup(p.unblock)

	// The burst runs on its own goroutine so a synchronous write path fails
	// the assertion instead of hanging the run: the stalled persister never
	// returns, so a blocking implementation would sit here forever.
	const burst = writeQueueDepth + 200
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < burst; i++ {
			StoreGeminiThoughtSignature(
				"call_burst_"+strconv.Itoa(i), "SIG_BURST_"+strconv.Itoa(i), "sess_burst", "gemini-2.5-pro")
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		p.unblock() // let the stuck goroutine finish before the test ends
		t.Fatalf("a %d-write burst did not return within 2s with the writer stalled; the write path is blocking on the backend", burst)
	}

	if got := globalThoughtSigStore.dropped.Load(); got == 0 {
		t.Errorf("no write was dropped with the queue full; the queue is not bounded at %d", writeQueueDepth)
	}

	// L1 must still serve the live session. The newest key is the one a
	// saturated writer would starve; the oldest has legitimately aged out of
	// the 2000-entry memory cap by the end of a burst this size.
	last := strconv.Itoa(burst - 1)
	if got := GetGeminiThoughtSignature("call_burst_"+last, "sess_burst", "gemini-2.5-pro"); got != "SIG_BURST_"+last {
		t.Errorf("L1 lost the newest entry during saturation: got %q", got)
	}
}

// TestThoughtSignatureStore_QueueDrainsOnShutdown is the §3.4 contract: a
// restart drains what was already accepted rather than discarding it.
func TestThoughtSignatureStore_QueueDrainsOnShutdown(t *testing.T) {
	repo := openTestSignatureRepo(t)
	p := &slowPersister{inner: repo, release: make(chan struct{})}
	t.Cleanup(func() { SetThoughtSignaturePersistence(nil) })

	SetThoughtSignaturePersistence(p)
	for i := 0; i < 5; i++ {
		StoreGeminiThoughtSignature("call_drain_"+strconv.Itoa(i), "SIG_DRAIN_"+strconv.Itoa(i), "sess_drain", "gemini-2.5-flash")
	}
	// Detaching stops the writer, which must drain before returning.
	p.unblock()
	start := time.Now()
	SetThoughtSignaturePersistence(nil)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("detaching persistence took %v; the writer did not drain", elapsed)
	}

	for i := 0; i < 5; i++ {
		row, err := repo.GetThoughtSignature(context.Background(), "call_drain_"+strconv.Itoa(i))
		if err != nil {
			t.Fatalf("read back %d: %v", i, err)
		}
		if row == nil {
			t.Errorf("call_drain_%d was accepted but never reached the database", i)
		}
	}
}

// TestThoughtSignatureStore_WritesAreSerialized pins the single-worker shape:
// with one goroutine owning the writes, no two saves overlap.
func TestThoughtSignatureStore_WritesAreSerialized(t *testing.T) {
	repo := openTestSignatureRepo(t)
	p := &concurrencyProbe{inner: repo}
	t.Cleanup(func() { SetThoughtSignaturePersistence(nil) })
	SetThoughtSignaturePersistence(p)

	for i := 0; i < 20; i++ {
		StoreGeminiThoughtSignature("call_serial_"+strconv.Itoa(i), "SIG", "sess_serial", "gemini-2.5-flash")
	}
	globalThoughtSigStore.drainSignatureWrites()

	if p.maxInflight.Load() > 1 {
		t.Errorf("%d saves ran concurrently; the design is one writer", p.maxInflight.Load())
	}
	if p.saves.Load() == 0 {
		t.Fatal("no save reached the persister")
	}
}

type concurrencyProbe struct {
	inner       *db.Repo
	inflight    atomic.Int64
	maxInflight atomic.Int64
	saves       atomic.Int64
}

func (c *concurrencyProbe) SaveThoughtSignature(ctx context.Context, key, signature, family string, expiresAt time.Time) error {
	n := c.inflight.Add(1)
	defer c.inflight.Add(-1)
	for {
		cur := c.maxInflight.Load()
		if n <= cur || c.maxInflight.CompareAndSwap(cur, n) {
			break
		}
	}
	time.Sleep(time.Millisecond)
	c.saves.Add(1)
	return c.inner.SaveThoughtSignature(ctx, key, signature, family, expiresAt)
}

func (c *concurrencyProbe) GetThoughtSignature(ctx context.Context, key string) (*db.ThoughtSignatureRow, error) {
	return c.inner.GetThoughtSignature(ctx, key)
}

func (c *concurrencyProbe) LoadThoughtSignatures(ctx context.Context, limit int) ([]db.ThoughtSignatureRow, error) {
	return c.inner.LoadThoughtSignatures(ctx, limit)
}

func (c *concurrencyProbe) PruneThoughtSignatures(ctx context.Context, now time.Time) (int64, error) {
	return c.inner.PruneThoughtSignatures(ctx, now)
}

func (c *concurrencyProbe) TrimThoughtSignatures(ctx context.Context, keep int) (int64, error) {
	return c.inner.TrimThoughtSignatures(ctx, keep)
}
