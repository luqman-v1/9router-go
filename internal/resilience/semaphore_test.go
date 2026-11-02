package resilience

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSemaphoreKey(t *testing.T) {
	// An empty proxy hash must not produce a trailing colon that collides with
	// a differently-shaped key.
	if got := SemaphoreKey("p", "a", ""); got != "p:a:direct" {
		t.Errorf("SemaphoreKey(empty proxy) = %q, want p:a:direct", got)
	}
	if got := SemaphoreKey("p", "a", "h"); got != "p:a:h" {
		t.Errorf("SemaphoreKey() = %q, want p:a:h", got)
	}
	// Two accounts sharing a proxy must not share a gate, so one dead proxy's
	// limit does not throttle the other.
	if SemaphoreKey("p", "a", "h") == SemaphoreKey("p", "b", "h") {
		t.Error("two accounts on one proxy produced the same gate key")
	}
}

func TestAcquireReleasesSlot(t *testing.T) {
	ResetSemaphores()
	release, err := Acquire(context.Background(), "k1", SemaphoreOptions{MaxConcurrency: 1})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	stats := SemaphoreStats()
	var running int
	for _, s := range stats {
		if s.Key == "k1" {
			running = s.Running
		}
	}
	if running != 1 {
		t.Errorf("running = %d, want 1 after Acquire", running)
	}
	release()
	// The gate is dropped once idle, so the registry must not grow forever.
	for _, s := range SemaphoreStats() {
		if s.Key == "k1" {
			t.Error("idle gate was not cleaned up")
		}
	}
}

func TestAcquireIsIdempotent(t *testing.T) {
	ResetSemaphores()
	release, err := Acquire(context.Background(), "k2", SemaphoreOptions{MaxConcurrency: 1})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	// Calling release twice must not over-decrement running and hand a phantom
	// slot to a later waiter.
	release()
	release()
	for _, s := range SemaphoreStats() {
		if s.Key == "k2" && s.Running != 0 {
			t.Errorf("running = %d, want 0 after two releases", s.Running)
		}
	}
}

func TestAcquireZeroConcurrencyBypasses(t *testing.T) {
	ResetSemaphores()
	release, err := Acquire(context.Background(), "k3", SemaphoreOptions{MaxConcurrency: 0})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	release() // no-op
	for _, s := range SemaphoreStats() {
		if s.Key == "k3" {
			t.Error("a bypassed gate was registered")
		}
	}
}

func TestAcquireQueuesBeyondCap(t *testing.T) {
	ResetSemaphores()
	first, err := Acquire(context.Background(), "k4", SemaphoreOptions{MaxConcurrency: 1, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	var wg sync.WaitGroup
	var gotSlot atomic.Bool
	wg.Add(1)
	go func() {
		defer wg.Done()
		release, err := Acquire(context.Background(), "k4", SemaphoreOptions{MaxConcurrency: 1, Timeout: 5 * time.Second})
		if err != nil {
			t.Errorf("second Acquire: %v", err)
			return
		}
		gotSlot.Store(true)
		release()
	}()

	// Give the goroutine time to enqueue, then prove it is waiting rather than
	// holding a slot.
	time.Sleep(50 * time.Millisecond)
	var queued int
	for _, s := range SemaphoreStats() {
		if s.Key == "k4" {
			queued = s.Queued
		}
	}
	if queued != 1 {
		t.Errorf("queued = %d, want 1 while the cap is held", queued)
	}
	if gotSlot.Load() {
		t.Error("second caller got a slot while the first still held it")
	}

	first()
	wg.Wait()
	if !gotSlot.Load() {
		t.Error("second caller never got a slot after the first released")
	}
}

func TestAcquireFullQueueRefused(t *testing.T) {
	ResetSemaphores()
	held, err := Acquire(context.Background(), "k5", SemaphoreOptions{MaxConcurrency: 1})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer held()

	// Fill the queue with waiters that will not be satisfied.
	for range 3 {
		_, err := Acquire(context.Background(), "k5", SemaphoreOptions{
			MaxConcurrency: 1,
			MaxQueueSize:   3,
			Timeout:        200 * time.Millisecond,
		})
		// These block until they time out, so run them in the background.
		_ = err
	}
	// The queue is now full (or draining); the next caller must be refused
	// outright rather than waiting.
	_, err = Acquire(context.Background(), "k5", SemaphoreOptions{
		MaxConcurrency: 1,
		MaxQueueSize:   1,
		Timeout:        100 * time.Millisecond,
	})
	if !errors.Is(err, ErrSemaphoreCapacity) {
		t.Errorf("Acquire on a full queue error = %v, want ErrSemaphoreCapacity", err)
	}
}

func TestAcquireTimeout(t *testing.T) {
	ResetSemaphores()
	held, err := Acquire(context.Background(), "k6", SemaphoreOptions{MaxConcurrency: 1})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer held()

	start := time.Now()
	_, err = Acquire(context.Background(), "k6", SemaphoreOptions{
		MaxConcurrency: 1,
		Timeout:        150 * time.Millisecond,
	})
	if !errors.Is(err, ErrSemaphoreCapacity) {
		t.Fatalf("Acquire error = %v, want ErrSemaphoreCapacity", err)
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Errorf("Acquire returned after %v, want it to wait for the timeout", elapsed)
	}
}

func TestAcquireContextCancel(t *testing.T) {
	ResetSemaphores()
	held, err := Acquire(context.Background(), "k7", SemaphoreOptions{MaxConcurrency: 1})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer held()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, err = Acquire(ctx, "k7", SemaphoreOptions{MaxConcurrency: 1, Timeout: 5 * time.Second})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Acquire error = %v, want context.Canceled", err)
	}
}

func TestBlockHoldsRequests(t *testing.T) {
	ResetSemaphores()
	key := "k8"
	Block(key, 300*time.Millisecond)

	// A request during the block must wait, not run.
	start := time.Now()
	release, err := Acquire(context.Background(), key, SemaphoreOptions{
		MaxConcurrency: 1,
		Timeout:        2 * time.Second,
	})
	if err != nil {
		t.Fatalf("Acquire during block: %v", err)
	}
	defer release()
	if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
		t.Errorf("Acquire ran after %v, want it to wait for the block to expire", elapsed)
	}
}

func TestBlockExtendsRatherThanShortens(t *testing.T) {
	ResetSemaphores()
	key := "k9"
	Block(key, 100*time.Millisecond)
	// A second, longer block must extend the window rather than shorten it.
	Block(key, 400*time.Millisecond)
	time.Sleep(150 * time.Millisecond)

	start := time.Now()
	release, err := Acquire(context.Background(), key, SemaphoreOptions{
		MaxConcurrency: 1,
		Timeout:        2 * time.Second,
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer release()
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Errorf("Acquire ran after %v, want the longer block to still apply", elapsed)
	}
}

func TestBlockZeroIsNoOp(t *testing.T) {
	ResetSemaphores()
	Block("k10", 0)
	release, err := Acquire(context.Background(), "k10", SemaphoreOptions{MaxConcurrency: 1})
	if err != nil {
		t.Fatalf("Acquire after a zero block: %v", err)
	}
	release()
}

func TestSemaphoreStatsReportsBlock(t *testing.T) {
	ResetSemaphores()
	key := "k11"
	Block(key, 500*time.Millisecond)
	var found bool
	for _, s := range SemaphoreStats() {
		if s.Key == key {
			found = true
			if s.BlockedUntil == "" {
				t.Error("BlockedUntil is empty while a block is live")
			}
		}
	}
	if !found {
		t.Error("SemaphoreStats() is missing the blocked gate")
	}
}
