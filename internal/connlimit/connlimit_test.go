package connlimit

import (
	"context"
	"errors"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConnLimit_BoundsConcurrency(t *testing.T) {
	l := New(2, 3)
	conn := "conn-bound"

	var (
		running int32
		peak    int32
		mu      sync.Mutex
		granted int
		refused int
	)

	const n = 6
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := l.Acquire(context.Background(), conn)
			if err != nil {
				atomic.AddInt32(&running, 0)
				mu.Lock()
				refused++
				mu.Unlock()
				return
			}
			mu.Lock()
			granted++
			mu.Unlock()
			v := atomic.AddInt32(&running, 1)
			for {
				old := atomic.LoadInt32(&peak)
				if v <= old || atomic.CompareAndSwapInt32(&peak, old, v) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond) // hold the slot like an in-flight request
			atomic.AddInt32(&running, -1)
			release()
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&peak); got > 2 {
		t.Errorf("peak concurrency %d, want at most %d", got, 2)
	}
	if granted != 2 {
		t.Errorf("granted %d slots, want exactly %d (the limit)", granted, 2)
	}
	if refused != n-2 {
		t.Errorf("refused %d, want %d labelled failures", refused, n-2)
	}
}

// TestConnLimit_ExtraRequestGetsLabelledError covers that the refusal is the
// labelled ErrConnLimit error.
func TestConnLimit_ExtraRequestGetsLabelledError(t *testing.T) {
	l := New(1, 3)
	conn := "conn-labelled"

	release, err := l.Acquire(context.Background(), conn)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer release()

	_, err = l.Acquire(context.Background(), conn)
	if !errors.Is(err, ErrConnLimit) {
		t.Fatalf("second acquire: expected ErrConnLimit, got %v", err)
	}
	if err == nil || err.Error() == "" {
		t.Fatal("expected a non-empty labelled error naming the limit")
	}
}

func TestBackoff_ProgressiveDelay(t *testing.T) {
	l := New(1, 3)
	conn := "conn-backoff"

	// Three consecutive generic 403s, each must not be a TOS violation.
	for i := 0; i < 3; i++ {
		l.RecordFailure(conn, http.StatusForbidden,
			[]byte(`{"error":{"code":403,"message":"rate limited"}}`))
	}

	// Backoff widens exponentially: 2s, 4s, 8s (base doubled per failure).
	if got := BackoffDelay(1); got != 2*time.Second {
		t.Errorf("BackoffDelay(1) = %v, want 2s", got)
	}
	if got := BackoffDelay(2); got != 4*time.Second {
		t.Errorf("BackoffDelay(2) = %v, want 4s", got)
	}
	if got := BackoffDelay(3); got != 8*time.Second {
		t.Errorf("BackoffDelay(3) = %v, want 8s", got)
	}
	if BackoffDelay(3) >= BackoffDelay(20) {
		t.Errorf("backoff must be capped; Delay(3)=%v Delay(20)=%v", BackoffDelay(3), BackoffDelay(20))
	}

	// The next attempt is inside a backoff window: Allow reports a positive
	// wait at least as long as the computed delay (no sleeping in this test).
	wait, allowed := l.Allow(conn)
	if allowed {
		t.Fatal("expected Allow to refuse during the backoff window")
	}
	if wait < BackoffDelay(3) {
		t.Errorf("wait %v < computed delay %v", wait, BackoffDelay(3))
	}
}

func TestBackoff_ResetsOnSuccess(t *testing.T) {
	l := New(1, 3)
	conn := "conn-reset"

	l.RecordFailure(conn, http.StatusForbidden, []byte(`{"error":{"code":403}}`))
	l.RecordFailure(conn, http.StatusForbidden, []byte(`{"error":{"code":403}}`))
	if wait, _ := l.Allow(conn); wait <= 0 {
		t.Fatal("expected a backoff wait after two failures")
	}

	// A successful response resets failures and backoff (not a token refresh).
	l.RecordSuccess(conn)
	wait, allowed := l.Allow(conn)
	if !allowed || wait != 0 {
		t.Fatalf("after success Allow = (%v, %v), want (true, 0)", wait, allowed)
	}
}

func TestTOSViolationBypassesBackoffAndBreaker(t *testing.T) {
	l := New(1, 3)
	conn := "conn-tos"

	tosBody := []byte(`{"error":{"code":"TOS_VIOLATION","message":"terms of service"}}`)

	// Not a TOS violation: wrong status.
	if IsTOSViolation(http.StatusTooManyRequests, tosBody) {
		t.Fatal("429 with TOS body must not be classified TOS_VIOLATION")
	}
	// Not a TOS violation: wrong body.
	if IsTOSViolation(http.StatusForbidden, []byte(`{"error":{"code":"other"}}`)) {
		t.Fatal("403 without TOS body must not be classified TOS_VIOLATION")
	}
	if !IsTOSViolation(http.StatusForbidden, tosBody) {
		t.Fatal("403 with TOS_VIOLATION body must be classified permanent")
	}

	// Repeated TOS_VIOLATIONs must not charge backoff or open the breaker.
	for i := 0; i < 5; i++ {
		l.RecordFailure(conn, http.StatusForbidden, tosBody)
	}
	if wait, _ := l.Allow(conn); wait != 0 {
		t.Errorf("after TOS violations Allow wait = %v, want 0 (bypasses backoff)", wait)
	}
	if _, allowed := l.Allow(conn); !allowed {
		t.Fatal("breaker must stay closed after TOS violations (#996 regression pin)")
	}
	if l.failuresOf(conn) != 0 {
		t.Errorf("failure count after TOS violations = %d, want 0", l.failuresOf(conn))
	}
}

func TestCircuitBreaker_OpenThenSingleProbe(t *testing.T) {
	l := New(1, 3)
	conn := "conn-breaker"

	// Use a controllable clock so no real sleeping is required.
	fakeNow := time.Now()
	l.now = func() time.Time { return fakeNow }

	// Three consecutive failures open the breaker.
	for i := 0; i < 3; i++ {
		l.RecordFailure(conn, http.StatusForbidden, []byte(`{"error":{"code":403}}`))
	}
	if l.stateOf(conn) != breakerOpen {
		t.Fatal("expected breaker OPEN after threshold failures")
	}

	// While open: zero upstream calls admitted.
	if wait, allowed := l.Allow(conn); allowed || wait == 0 {
		t.Fatalf("open breaker Allow = (%v, %v), want (false, >0)", wait, allowed)
	}

	// Advance past cool-down: exactly one half-open probe is admitted.
	fakeNow = fakeNow.Add(DefaultCoolDown)
	if wait, allowed := l.Allow(conn); !allowed || wait != 0 {
		t.Fatalf("after cool-down Allow = (%v, %v), want (true, 0) single probe", wait, allowed)
	}
	if l.stateOf(conn) != breakerHalfOpen {
		t.Fatal("expected breaker HALF_OPEN after cool-down")
	}
	// The single probe was consumed by the transition itself: a second request
	// is refused (exactly one probe per open cycle).
	if _, allowed := l.Allow(conn); allowed {
		t.Fatal("second request during half-open must be refused (exactly one probe)")
	}

	// The probe succeeds: breaker releases.
	l.RecordSuccess(conn)
	if l.stateOf(conn) != breakerClosed {
		t.Fatal("expected breaker CLOSED after successful probe")
	}
	if wait, allowed := l.Allow(conn); !allowed || wait != 0 {
		t.Fatalf("after release Allow = (%v, %v), want (true, 0)", wait, allowed)
	}
}

func TestCircuitBreaker_FailedProbeReopens(t *testing.T) {
	l := New(1, 3)
	conn := "conn-breaker-reopen"
	fakeNow := time.Now()
	l.now = func() time.Time { return fakeNow }

	for i := 0; i < 3; i++ {
		l.RecordFailure(conn, http.StatusForbidden, []byte(`{"error":{"code":403}}`))
	}
	fakeNow = fakeNow.Add(DefaultCoolDown)
	if _, allowed := l.Allow(conn); !allowed {
		t.Fatal("expected a half-open probe after cool-down")
	}
	// The probe fails: breaker reopens.
	l.RecordFailure(conn, http.StatusForbidden, []byte(`{"error":{"code":403}}`))
	if l.stateOf(conn) != breakerOpen {
		t.Fatal("expected breaker OPEN again after failed probe")
	}
}

func TestCloseEvictsConnectionState(t *testing.T) {
	l := New(1, 3)
	conn := "conn-evict"

	l.RecordFailure(conn, http.StatusForbidden, []byte(`{"error":{"code":403}}`))
	if got := l.connCount(); got != 1 {
		t.Fatalf("after failure conn count = %d, want 1", got)
	}

	l.Close(conn)
	if got := l.connCount(); got != 0 {
		t.Fatalf("after Close conn count = %d, want 0 (state evicted, no leak)", got)
	}
}

func TestNoGoroutineLeak(t *testing.T) {
	before := runtime.NumGoroutine()

	l := New(3, 3)
	conn := "conn-leak"
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := l.Acquire(context.Background(), conn)
			if err != nil {
				return
			}
			release()
		}()
	}
	wg.Wait()
	l.Close(conn)

	// connlimit spawns no goroutines; timers are stopped via defer.
	after := runtime.NumGoroutine()
	if after > before+2 { // small tolerance for scheduler/test noise
		t.Fatalf("goroutines before=%d after=%d (possible leak)", before, after)
	}
}

// failuresOf reads the unexported failure count (same-package test).
func (l *Limiter) failuresOf(connID string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.conns[connID]
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failures
}

// stateOf reads the unexported breaker state.
func (l *Limiter) stateOf(connID string) breakerState {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.conns[connID]
	if s == nil {
		return breakerClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.breaker
}

// connCount reads the unexported registry size.
func (l *Limiter) connCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.conns)
}
