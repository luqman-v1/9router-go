package middleware

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestQueueIsFIFO proves parked requests are granted in arrival order. The
// reference's queue is explicitly FIFO (queue.push + pump shifts the head); a
// promise-based or last-in-first-out implementation would let a later caller
// overtake an earlier one, which is the fairness the queue exists to provide.
func TestQueueIsFIFO(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	const key = "fifo"

	// Hold the only concurrency slot.
	if _, ok := rl.Acquire(context.Background(), key, 0, 1, 0); !ok {
		t.Fatal("failed to take the only slot")
	}

	const n = 8
	order := make([]int, 0, n)
	var mu sync.Mutex
	var wg sync.WaitGroup

	// Enqueue n waiters in a strict order. Each records its index as it is
	// granted, then releases immediately so the next head can proceed.
	for i := range n {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if _, ok := rl.Acquire(context.Background(), key, 0, 1, 3*time.Second); !ok {
				t.Errorf("waiter %d timed out", idx)
				return
			}
			mu.Lock()
			order = append(order, idx)
			mu.Unlock()
			rl.ReleaseConcurrency(key)
		}(i)

		// Establish enqueue order instead of hoping for it: the next waiter
		// starts only once this one is parked. A sleep-based stagger used to
		// stand in for this and it broke under load — goroutine 4 reached
		// Acquire before 2 and 3, so the recorder saw 0,1,4,2,3,... and
		// reported a FIFO violation the queue had not made. The queue is what
		// this test is about, not the scheduler.
		waitParked(t, rl, key, i+1)
	}

	// Every waiter is parked; freeing the slot hands it down the queue.
	rl.ReleaseConcurrency(key)
	wg.Wait()

	if len(order) != n {
		t.Fatalf("granted %d waiters, want %d", len(order), n)
	}
	for i, got := range order {
		if got != i {
			t.Errorf("grant order = %v, want 0..%d in order (FIFO violated at position %d)", order, n-1, i)
			break
		}
	}
}

// waitParked blocks until the queue for key holds at least want waiters. It is
// white-box on purpose: the only observable "this request is queued" state is
// the queue itself, and a timing assumption here would test the scheduler
// rather than the limiter.
func waitParked(t *testing.T, rl *RateLimiter, key string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rl.mu.Lock()
		depth := 0
		if q, ok := rl.queues[key]; ok {
			depth = len(q.waiters)
		}
		rl.mu.Unlock()
		if depth >= want {
			return
		}
		time.Sleep(200 * time.Microsecond)
	}
	t.Fatalf("queue never held %d parked waiters", want)
}

// TestQueueWaitsForRPMWindow proves a request queues on RPM exhaustion, not just
// concurrency — the reference's queue admits on window rollover too. The window
// is short so the test does not sleep a full minute.
func TestQueueWaitsForRPMWindow(t *testing.T) {
	rl := NewRateLimiter(120 * time.Millisecond) // short window
	const key = "rpm-queue"

	// Fill the window: 1 request per 120ms.
	if _, ok := rl.Acquire(context.Background(), key, 1, 0, 0); !ok {
		t.Fatal("first request refused")
	}

	// The second cannot be admitted now (window full) but must be granted when
	// the window rolls, well inside a generous timeout.
	start := time.Now()
	waited, ok := rl.Acquire(context.Background(), key, 1, 0, 2*time.Second)
	if !ok {
		t.Fatal("second request timed out waiting for the RPM window")
	}
	if waited < 50*time.Millisecond {
		t.Errorf("waited only %v; expected to wait for the window to roll", waited)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("waited %v, far longer than the window", elapsed)
	}
}

// TestQueueRPMTimeoutRefuses proves a request that cannot get an RPM slot within
// its timeout is refused rather than served late.
func TestQueueRPMTimeoutRefuses(t *testing.T) {
	rl := NewRateLimiter(time.Minute) // 60s window: will not roll during the test
	const key = "rpm-timeout"

	if _, ok := rl.Acquire(context.Background(), key, 1, 0, 0); !ok {
		t.Fatal("first request refused")
	}

	start := time.Now()
	_, ok := rl.Acquire(context.Background(), key, 1, 0, 40*time.Millisecond)
	if ok {
		t.Fatal("second request admitted despite a full 60s window")
	}
	if elapsed := time.Since(start); elapsed < 30*time.Millisecond {
		t.Errorf("refused after %v, before its own timeout", elapsed)
	}
}

// TestQueueAdmitIsAtomicOnBothAxes proves a request refused on concurrency does
// not consume an RPM stamp. If it did, concurrency pressure would silently
// drain the RPM budget, and a key blocked on concurrency would also start
// getting RPM 429s once its window filled with refused requests.
func TestQueueAdmitIsAtomicOnBothAxes(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	const key = "atomic"

	// RPM room for 5 requests, concurrency cap of 1.
	if _, ok := rl.Acquire(context.Background(), key, 5, 1, 0); !ok {
		t.Fatal("first request refused")
	}

	// Ten more requests, all refused on concurrency (slot held, zero timeout).
	for i := 0; i < 10; i++ {
		if _, ok := rl.Acquire(context.Background(), key, 5, 1, 0); ok {
			t.Fatal("request admitted despite the slot being held")
		}
	}

	// The RPM window must have exactly ONE stamp (the first request), not
	// eleven. A second RPM slot must therefore still be free.
	rl.mu.Lock()
	stamps := len(rl.windows[key].stamps)
	rl.mu.Unlock()
	if stamps != 1 {
		t.Errorf("RPM window holds %d stamps, want 1 (refused requests must not consume RPM)", stamps)
	}
}

// TestQueueSoakNoQueueLeak proves the queue map does not grow without bound when
// waiters come and go: after a burst of queued-then-timed-out requests, the map
// entry is reclaimed.
func TestQueueSoakNoQueueLeak(t *testing.T) {
	rl := NewRateLimiter(time.Minute)

	for i := 0; i < 500; i++ {
		key := keyName(i)
		// Take the slot, queue a second request with a hair of patience, then
		// let both finish.
		if _, ok := rl.Acquire(context.Background(), key, 0, 1, 0); !ok {
			t.Fatalf("iteration %d: slot not taken", i)
		}
		rl.Acquire(context.Background(), key, 0, 1, time.Millisecond) // parks then times out
		rl.ReleaseConcurrency(key)
	}

	rl.mu.Lock()
	queued := len(rl.queues)
	rl.mu.Unlock()
	if queued != 0 {
		t.Errorf("queue map holds %d entries after all waiters drained, want 0", queued)
	}
}
