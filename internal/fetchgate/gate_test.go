package fetchgate

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"
)

// An idle gate must not delay the single caller: the dashboard's per-provider
// refresh button pays nothing, only the burst behind it is paced.
func TestGateAcquire_IdleGateReturnsImmediately(t *testing.T) {
	g := New(250*time.Millisecond, 120*time.Millisecond)

	start := time.Now()
	if err := g.Acquire(t.Context()); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("idle gate delayed the first caller by %s", elapsed)
	}
}

// The whole point of the gate: N simultaneous callers must not start in the
// same millisecond. This is the ten-accounts-on-one-IP case from issue #30.
//
// The assertion is on the span of the whole burst, not on each gap
// individually. A recorded start is "the instant the gate opened my slot" plus
// "however long my goroutine then waited to be scheduled onto a busy CPU", and
// that second term is unbounded — differencing two consecutive starts compares
// two different goroutines' scheduling luck. One early goroutine next to one
// late goroutine reads as two slots at once even though the gate did nothing
// wrong, which is why this failed at 24-33ms against a 40ms floor under
// `-p 16`, always on the same middle indices where the two kinds of luck meet.
//
// Summing the gaps cancels that noise instead: scheduler lag is zero-sum
// around the loop. Eight callers on a 40ms floor span at least 280ms, while a
// gate that granted them all at once spans the same few microseconds however
// busy the CPU is. The span is therefore invariant under scheduling jitter and
// still collapses the moment pacing is removed.
func TestGateAcquire_SpacesConcurrentCallers(t *testing.T) {
	const (
		callers = 8
		minGap  = 40 * time.Millisecond
	)
	g := New(minGap, 0)

	var (
		mu    sync.Mutex
		slots []time.Duration
		wg    sync.WaitGroup
	)
	start := time.Now()
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := g.Acquire(t.Context()); err != nil {
				t.Errorf("Acquire: %v", err)
				return
			}
			mu.Lock()
			slots = append(slots, time.Since(start))
			mu.Unlock()
		}()
	}
	wg.Wait()

	if len(slots) != callers {
		t.Fatalf("got %d slots, want %d", len(slots), callers)
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i] < slots[j] })

	// callers-1 gaps of at least minGap: anything shorter means the gate
	// let at least one pair through without waiting, which is the burst this
	// gate exists to prevent.
	span := slots[len(slots)-1] - slots[0]
	wantSpan := minGap * time.Duration(callers-1)
	if span < wantSpan {
		t.Errorf("%d callers spanned %s end to end, want at least %s: they started %s too close together",
			callers, span, wantSpan, span/time.Duration(callers-1))
	}
}

// Jitter must only ever add delay: the floor stays the hard guarantee.
//
// Checked gap by gap rather than on the span, because a span can hide a
// halved floor: jitter is drawn from [0, maxJitter], so enough generous draws
// still stretch the total past the target while every individual gap is short.
//
// The floor is measured with a stopwatch, so it needs margin for a late
// goroutine wake-up. At 30ms it had none — jitter may legally be 0, making the
// next grant due exactly 30ms out — and under -race with the rest of the suite
// loaded that came back as 29.77ms once in five runs with the gate behaving
// correctly throughout.
func TestGateAcquire_JitterOnlyWidensTheGap(t *testing.T) {
	const (
		minGap = 40 * time.Millisecond
		jitter = 60 * time.Millisecond
		slots  = 6
	)
	g := New(minGap, jitter)

	if err := g.Acquire(t.Context()); err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	for i := 1; i < slots; i++ {
		start := time.Now()
		if err := g.Acquire(t.Context()); err != nil {
			t.Fatalf("Acquire %d: %v", i, err)
		}
		if elapsed := time.Since(start); elapsed < minGap {
			t.Fatalf("slot %d waited %s, want at least the %s floor", i, elapsed, minGap)
		}
	}
}

// A dashboard that navigates away mid-wait must not hold a slot against the
// requests behind it, and it must see its own cancellation as an error.
func TestGateAcquire_CanceledContextReleasesItsWait(t *testing.T) {
	g := New(200*time.Millisecond, 0)

	if err := g.Acquire(t.Context()); err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := g.Acquire(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire error = %v, want context.Canceled", err)
	}
}

// A zero gap is a legitimate configuration (pure jitter, no floor), and must
// not be mistaken for a misconfigured gate.
func TestGateAcquire_ZeroGapStillGates(t *testing.T) {
	g := New(0, 0)

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := g.Acquire(t.Context()); err != nil {
				t.Errorf("Acquire: %v", err)
			}
		}()
	}
	wg.Wait()
}
