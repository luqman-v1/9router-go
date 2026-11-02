package resilience

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Semaphore is a FIFO concurrency limiter keyed by provider:account:proxy.
//
// A request beyond the cap waits for a slot instead of failing, up to a timeout;
// the queue is bounded so a burst cannot grow without limit. This mirrors
// VansRouter's open-sse/services/accountSemaphore.js and exists to stop one
// account from being hit with more concurrent requests than it can serve, which
// is how a 429 cascade starts.
//
// A gate can also be blocked outright (Block) when the account is known to be
// rate-limited, so queued waiters are held until the block expires.

// ErrSemaphoreCapacity means the queue was full or the wait timed out.
var ErrSemaphoreCapacity = errors.New("semaphore capacity reached")

// SemaphoreOptions configures a gate. Zero values take the defaults.
type SemaphoreOptions struct {
	MaxConcurrency int
	Timeout        time.Duration
	MaxQueueSize   int
}

const (
	defaultMaxConcurrency = 3
	defaultTimeout        = 30 * time.Second
	defaultMaxQueueSize   = 20
)

// SemaphoreKey builds the gate key. proxyHash separates accounts that share a
// proxy so one dead proxy's limit does not throttle another's.
func SemaphoreKey(provider, accountKey, proxyHash string) string {
	if proxyHash == "" {
		proxyHash = "direct"
	}
	return provider + ":" + accountKey + ":" + proxyHash
}

type waiter struct {
	ready chan func()
}

type gate struct {
	mu             sync.Mutex
	running        int
	maxConcurrency int
	queue          []*waiter
	blockedUntil   time.Time
}

// registry holds one gate per key.
var (
	semMu    sync.Mutex
	semGates = map[string]*gate{}
)

func gateFor(key string, maxConcurrency int) *gate {
	semMu.Lock()
	defer semMu.Unlock()
	if g, ok := semGates[key]; ok {
		g.mu.Lock()
		g.maxConcurrency = maxConcurrency
		g.mu.Unlock()
		return g
	}
	g := &gate{maxConcurrency: maxConcurrency}
	semGates[key] = g
	return g
}

// cleanupGate drops an idle gate so the registry does not grow forever. A gate
// with waiters or a live block is kept.
func cleanupGate(key string, g *gate) {
	g.mu.Lock()
	idle := g.running == 0 && len(g.queue) == 0 &&
		(g.blockedUntil.IsZero() || time.Now().After(g.blockedUntil))
	g.mu.Unlock()
	if !idle {
		return
	}
	semMu.Lock()
	// Re-check under the registry lock: a concurrent acquire may have taken it.
	g.mu.Lock()
	stillIdle := g.running == 0 && len(g.queue) == 0 &&
		(g.blockedUntil.IsZero() || time.Now().After(g.blockedUntil))
	g.mu.Unlock()
	if stillIdle && semGates[key] == g {
		delete(semGates, key)
	}
	semMu.Unlock()
}

// Acquire reserves a slot and returns the release function. The caller MUST
// call release exactly once (it is safe to call more than once).
//
// A MaxConcurrency of <= 0 bypasses the limiter entirely, returning a no-op
// release.
func Acquire(ctx context.Context, key string, opts SemaphoreOptions) (func(), error) {
	if opts.MaxConcurrency <= 0 {
		return func() {}, nil
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaultTimeout
	}
	if opts.MaxQueueSize <= 0 {
		opts.MaxQueueSize = defaultMaxQueueSize
	}
	g := gateFor(key, opts.MaxConcurrency)
	g.mu.Lock()
	// Fast path: a free slot and no active block.
	blocked := !g.blockedUntil.IsZero() && time.Now().Before(g.blockedUntil)
	if !blocked && g.running < g.maxConcurrency {
		g.running++
		g.mu.Unlock()
		return makeRelease(key, g), nil
	}
	// A full queue is refused outright rather than waiting for a timeout.
	if len(g.queue) >= opts.MaxQueueSize {
		g.mu.Unlock()
		return nil, fmt.Errorf("%w: queue full for %q", ErrSemaphoreCapacity, key)
	}
	w := &waiter{ready: make(chan func(), 1)}
	g.queue = append(g.queue, w)
	g.mu.Unlock()

	timer := time.NewTimer(opts.Timeout)
	defer timer.Stop()
	select {
	case release := <-w.ready:
		return release, nil
	case <-timer.C:
		if g.removeWaiter(w) {
			return nil, fmt.Errorf("%w: timed out after %s for %q", ErrSemaphoreCapacity, opts.Timeout, key)
		}
		// The slot was granted in the same instant the timer fired: honour it
		// rather than leaking a reservation.
		return <-w.ready, nil
	case <-ctx.Done():
		if g.removeWaiter(w) {
			return nil, ctx.Err()
		}
		return <-w.ready, nil
	}
}

// removeWaiter takes w out of the queue, reporting whether it was still there.
func (g *gate) removeWaiter(w *waiter) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i, queued := range g.queue {
		if queued == w {
			g.queue = append(g.queue[:i], g.queue[i+1:]...)
			return true
		}
	}
	return false
}

// makeRelease returns the idempotent release function for a granted slot.
func makeRelease(key string, g *gate) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			g.running--
			g.mu.Unlock()
			drain(key, g)
			cleanupGate(key, g)
		})
	}
}

// drain hands free slots to queued waiters, stopping at an active block.
func drain(key string, g *gate) {
	for {
		g.mu.Lock()
		if len(g.queue) == 0 || g.running >= g.maxConcurrency {
			g.mu.Unlock()
			return
		}
		if !g.blockedUntil.IsZero() && time.Now().Before(g.blockedUntil) {
			g.mu.Unlock()
			return
		}
		w := g.queue[0]
		g.queue = g.queue[1:]
		g.running++
		g.mu.Unlock()
		// A waiter that timed out removes itself from the queue under the same
		// lock, so anything still queued here is live and must receive its
		// slot — otherwise the reservation would leak.
		w.ready <- makeRelease(key, g)
	}
}

// Block holds every request on a gate for the given duration. Used when an
// account returns 429 so queued and future requests wait instead of piling on.
//
// The gate is created if it does not exist yet, so a 429 that arrives before
// the first acquire still takes effect.
func Block(key string, duration time.Duration) {
	if duration <= 0 {
		return
	}
	g := gateFor(key, defaultMaxConcurrency)
	until := time.Now().Add(duration)
	g.mu.Lock()
	if g.blockedUntil.Before(until) {
		g.blockedUntil = until
	}
	g.mu.Unlock()
	// Wake up when the block expires so queued waiters are not stuck until the
	// next Acquire call.
	go func() {
		timer := time.NewTimer(duration)
		defer timer.Stop()
		<-timer.C
		drain(key, g)
		cleanupGate(key, g)
	}()
}

// SemaphoreStat is a snapshot of one gate, for the dashboard.
type SemaphoreStat struct {
	Key            string `json:"key"`
	Running        int    `json:"running"`
	Queued         int    `json:"queued"`
	MaxConcurrency int    `json:"maxConcurrency"`
	BlockedUntil   string `json:"blockedUntil,omitempty"`
}

// SemaphoreStats returns a snapshot of every live gate.
func SemaphoreStats() []SemaphoreStat {
	semMu.Lock()
	gates := make(map[string]*gate, len(semGates))
	for k, g := range semGates {
		gates[k] = g
	}
	semMu.Unlock()
	out := make([]SemaphoreStat, 0, len(gates))
	for k, g := range gates {
		g.mu.Lock()
		st := SemaphoreStat{
			Key:            k,
			Running:        g.running,
			Queued:         len(g.queue),
			MaxConcurrency: g.maxConcurrency,
		}
		if !g.blockedUntil.IsZero() && time.Now().Before(g.blockedUntil) {
			st.BlockedUntil = g.blockedUntil.UTC().Format(time.RFC3339)
		}
		g.mu.Unlock()
		out = append(out, st)
	}
	return out
}

// ResetSemaphores clears every gate. Used by tests.
func ResetSemaphores() {
	semMu.Lock()
	semGates = map[string]*gate{}
	semMu.Unlock()
}
