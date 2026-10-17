package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"9router/proxy/internal/models"
)

// TestAcquireLeavesQueueOnContextCancel is the regression for the reviewer's
// blocking issue #2: a caller that hangs up must leave the queue, not stay
// parked and then be granted a slot and dispatched upstream for nobody.
func TestAcquireLeavesQueueOnContextCancel(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	const key = "ctx-cancel"

	// Hold the only slot so the next acquire has to park.
	if _, ok := rl.Acquire(context.Background(), key, 0, 1, 0); !ok {
		t.Fatal("failed to take the only slot")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() {
		_, ok := rl.Acquire(ctx, key, 0, 1, 5*time.Second)
		done <- ok
	}()

	// Let it park, then cancel the caller.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case ok := <-done:
		if ok {
			t.Error("Acquire returned true for a cancelled caller; it should not hold a slot")
		}
	case <-time.After(time.Second):
		t.Fatal("Acquire did not return after the context was cancelled")
	}

	// The waiter must have been discarded: the queue is empty and the slot is
	// still held by the original request only.
	rl.mu.Lock()
	queued := 0
	if q := rl.queues[key]; q != nil {
		queued = len(q.waiters)
	}
	inFlight := int32(0)
	if c := rl.active[key]; c != nil {
		inFlight = atomic.LoadInt32(c)
	}
	rl.mu.Unlock()
	if queued != 0 {
		t.Errorf("queue holds %d waiters after the caller cancelled, want 0", queued)
	}
	if inFlight != 1 {
		t.Errorf("in-flight = %d, want 1 (the cancelled caller leaked a slot)", inFlight)
	}
}

// TestAcquireAlreadyCancelledContextDoesNotPark proves a caller whose context is
// already done never enters the queue at all.
func TestAcquireAlreadyCancelledContextDoesNotPark(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	const key = "ctx-pre"

	if _, ok := rl.Acquire(context.Background(), key, 0, 1, 0); !ok {
		t.Fatal("failed to take the only slot")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already done

	// A done context must be refused on the fast path, not after parking until
	// the timeout: with a 5s budget, returning in well under that is the proof
	// it never entered the queue. (Asserting only the return value would pass
	// even if the request parked the full 5s and then timed out.)
	start := time.Now()
	_, ok := rl.Acquire(ctx, key, 0, 1, 5*time.Second)
	elapsed := time.Since(start)
	if ok {
		t.Fatal("Acquire admitted a caller whose context was already cancelled")
	}
	if elapsed > time.Second {
		t.Errorf("Acquire took %v for an already-cancelled context; it should refuse at once, not park", elapsed)
	}
	rl.mu.Lock()
	queued := 0
	if q := rl.queues[key]; q != nil {
		queued = len(q.waiters)
	}
	rl.mu.Unlock()
	if queued != 0 {
		t.Errorf("queue holds %d waiters, want 0 (a done context must not park)", queued)
	}
}

// TestRefundTPMReturnsQueueRefusalCharge is the regression for the reviewer's
// issue #3: TPM is charged before the queue, so a queue refusal must give the
// charge back. Without it a key under a flood spends its token budget on turns
// that were never served.
func TestRefundTPMReturnsQueueRefusalCharge(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	const key = "tpm-refund"

	// Charge the bucket directly, as the middleware would before Acquire.
	if !rl.checkTPM(key, 1000, 400, time.Now()) {
		t.Fatal("initial TPM charge refused")
	}
	rl.mu.Lock()
	afterCharge := rl.tokens[key].tokens
	rl.mu.Unlock()
	if afterCharge >= 1000 {
		t.Fatalf("charge did not reduce the bucket: %v", afterCharge)
	}

	rl.RefundTPM(key, 1000, 400)

	rl.mu.Lock()
	afterRefund := rl.tokens[key].tokens
	rl.mu.Unlock()
	if afterRefund <= afterCharge {
		t.Errorf("refund did not return tokens: %v -> %v", afterCharge, afterRefund)
	}
	if afterRefund > 1000 {
		t.Errorf("refund exceeded the bucket limit: %v", afterRefund)
	}
}

// TestRefundTPMIsCappedAtBucketLimit proves a refund can never create more
// headroom than the key was ever given, so a bad refund cannot loosen the limit.
func TestRefundTPMIsCappedAtBucketLimit(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	const key = "tpm-cap"

	if !rl.checkTPM(key, 100, 10, time.Now()) {
		t.Fatal("initial charge refused")
	}
	// Refund far more than was charged.
	rl.RefundTPM(key, 100, 100000)

	rl.mu.Lock()
	tokens := rl.tokens[key].tokens
	rl.mu.Unlock()
	if tokens > 100 {
		t.Errorf("bucket = %v after an oversized refund, want <= 100", tokens)
	}
}

// TestQueueDepthCapRefuses proves a saturated key's queue is bounded: past the
// cap a request is refused immediately instead of parking another goroutine.
func TestQueueDepthCapRefuses(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	rl.maxQueueDepth = 3

	const key = "depth"
	if _, ok := rl.Acquire(context.Background(), key, 0, 1, 0); !ok {
		t.Fatal("failed to take the only slot")
	}

	// Fill the queue to the cap; each parks for a long time.
	for i := 0; i < 3; i++ {
		go rl.Acquire(context.Background(), key, 0, 1, 3*time.Second)
	}
	waitForQueueLen(t, rl, key, 3)

	// The next request must be refused immediately, not parked.
	start := time.Now()
	_, ok := rl.Acquire(context.Background(), key, 0, 1, 3*time.Second)
	if ok {
		t.Fatal("Acquire succeeded past the depth cap")
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Errorf("refusal took %v; a full queue must refuse at once, not park", elapsed)
	}
}

// TestQueueDepthCapThroughMiddleware proves the middleware never parks past the
// cap: the excess request gets a 429 rather than waiting.
func TestQueueDepthCapThroughMiddleware(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	rl.maxQueueDepth = 1

	block := make(chan struct{})
	entered := make(chan struct{}, 1)
	handler := RequireRateLimit(rl, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-block
		w.WriteHeader(http.StatusOK)
	}))

	mkReq := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		return req.WithContext(withAPIKey(req, &models.APIKey{
			ID:                   "depthmw",
			RateLimitConcurrency: new(1),
			QueueTimeoutMs:       new(5000),
		}))
	}

	// Request 1 holds the slot and blocks in the handler.
	go func() {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, mkReq())
	}()
	<-entered

	// Request 2 fills the only queue slot (parks).
	go func() {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, mkReq())
	}()
	waitForQueueLen(t, rl, "depthmw", 1)

	// Request 3 must be refused at once with 429.
	start := time.Now()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, mkReq())
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("third request status = %d, want 429", rec.Code)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("third request took %v; a full queue must refuse fast", elapsed)
	}
	close(block)
}

// TestEvictionSparesAdmittingKey is the regression for the reviewer's issue #6:
// the eviction sweep runs from inside the admit (recordStampLocked) and, at the
// maxKeys boundary, could reclaim the very window the admit is about to stamp —
// the stamp then lands in a fresh window and the key is admitted past its own
// RPM limit. The fix passes the admitting key to the sweep as a skip.
//
// It is asserted on the sweep directly, because that is the unit that changed:
// an end-to-end reproduction depends on which idle keys the sweep happens to
// pick, which is not deterministic. Here the map is exactly at the cap and the
// spare key is named, so the only question is whether the sweep honours it.
func TestEvictionSparesAdmittingKey(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	rl.maxKeys = 4

	const keep = "admitting-key"
	// Fill the map to the cap, the admitting key among them.
	for _, name := range []string{keep, "idle-1", "idle-2", "idle-3"} {
		rl.mu.Lock()
		rl.windowForLocked(name)
		rl.mu.Unlock()
	}

	rl.mu.Lock()
	before := len(rl.windows)
	rl.evictIfNeededLocked(keep)
	after := len(rl.windows)
	_, kept := rl.windows[keep]
	rl.mu.Unlock()

	if !kept {
		t.Fatal("the admitting key was evicted by its own admit's sweep")
	}
	if after >= before {
		t.Errorf("sweep did not reclaim anything: %d -> %d (test is not exercising eviction)", before, after)
	}
	if after > rl.maxKeys*9/10 {
		t.Errorf("sweep left %d entries, want <= %d", after, rl.maxKeys*9/10)
	}
}

// TestQueueDepthCapExactBoundary proves the cap is "at or above": with a cap of
// 2, exactly 2 waiters park and the 3rd is refused. An off-by-one here would
// either waste a slot of capacity or admit one goroutine past the bound.
func TestQueueDepthCapExactBoundary(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	rl.maxQueueDepth = 2

	const key = "depth-exact"
	if _, ok := rl.Acquire(context.Background(), key, 0, 1, 0); !ok {
		t.Fatal("failed to take the only slot")
	}

	// Park exactly two.
	go rl.Acquire(context.Background(), key, 0, 1, 3*time.Second)
	waitForQueueLen(t, rl, key, 1)
	go rl.Acquire(context.Background(), key, 0, 1, 3*time.Second)
	waitForQueueLen(t, rl, key, 2)

	rl.mu.Lock()
	queued := len(rl.queues[key].waiters)
	rl.mu.Unlock()
	if queued != 2 {
		t.Fatalf("queue holds %d waiters at the cap, want 2", queued)
	}

	// The third is refused at once.
	if _, ok := rl.Acquire(context.Background(), key, 0, 1, 100*time.Millisecond); ok {
		t.Fatal("Acquire admitted a request past the exact cap")
	}
}

// TestQueueDepthZeroFallsBackToDefault proves a hand-built RateLimiter with a
// zero depth does not refuse every request: the cap falls back to the default
// rather than comparing against zero.
func TestQueueDepthZeroFallsBackToDefault(t *testing.T) {
	rl := &RateLimiter{
		windows:       nil,
		tokens:        nil,
		active:        map[string]*int32{},
		queues:        map[string]*waitQueue{},
		windowTimers:  map[string]*time.Timer{},
		window:        time.Minute,
		maxKeys:       10,
		maxQueueDepth: 0,
	}
	const key = "zero-cap"
	if _, ok := rl.Acquire(context.Background(), key, 0, 1, 0); !ok {
		t.Fatal("failed to take the only slot")
	}
	// This must park (default cap allows it), not refuse.
	go rl.Acquire(context.Background(), key, 0, 1, 3*time.Second)
	waitForQueueLen(t, rl, key, 1)
}

// TestCancelledWaiterFreesItsQueueSlot proves a waiter that leaves on cancel
// does not occupy queue depth: after it goes, the queue is empty and a later
// request can park again. This ties fix #2 (cancel) to fix #5 (depth).
func TestCancelledWaiterFreesItsQueueSlot(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	rl.maxQueueDepth = 1

	const key = "cancel-free"
	if _, ok := rl.Acquire(context.Background(), key, 0, 1, 0); !ok {
		t.Fatal("failed to take the only slot")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		rl.Acquire(ctx, key, 0, 1, 5*time.Second)
	}()
	waitForQueueLen(t, rl, key, 1)

	// The queue is full, so another request is refused.
	if _, ok := rl.Acquire(context.Background(), key, 0, 1, 50*time.Millisecond); ok {
		t.Fatal("queue was not full despite one parked waiter")
	}

	// Cancel the parked waiter; it must release its queue slot.
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled waiter did not return")
	}
	waitForQueueLen(t, rl, key, 0) // drains to 0

	// Now a fresh request can park again (the slot was freed).
	go rl.Acquire(context.Background(), key, 0, 1, 2*time.Second)
	waitForQueueLen(t, rl, key, 1)
}

// TestRefundTPMNoBucketIsSafe proves a refund for a key with no token bucket is
// a no-op, not a panic or a spurious bucket creation.
func TestRefundTPMNoBucketIsSafe(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	rl.RefundTPM("never-charged", 1000, 500)
	if _, exists := rl.tokens["never-charged"]; exists {
		t.Error("refund created a token bucket for a key that was never charged")
	}
	// Non-positive amounts and tpm are also no-ops.
	rl.RefundTPM("k", 0, 100)
	rl.RefundTPM("k", 100, 0)
	rl.RefundTPM("", 100, 100)
}

// TestRefundTPMThenSettleIsConsistent proves a refund followed by a settle
// cannot push the bucket above its limit, and settle still only corrects an
// under-estimate.
func TestRefundTPMThenSettleIsConsistent(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	const key = "refund-settle"

	// Charge 300 of a 1000 bucket.
	if !rl.checkTPM(key, 1000, 300, time.Now()) {
		t.Fatal("charge refused")
	}
	// Refund it all (the request was refused by the queue).
	rl.RefundTPM(key, 1000, 300)
	rl.mu.Lock()
	afterRefund := rl.tokens[key].tokens
	rl.mu.Unlock()
	if afterRefund > 1000 {
		t.Fatalf("bucket = %v after refund, want <= 1000", afterRefund)
	}

	// A settle that claims an under-estimate must not exceed the limit either.
	rl.SettleTokenUsage(key, 1000, 0, 100000)
	rl.mu.Lock()
	afterSettle := rl.tokens[key].tokens
	rl.mu.Unlock()
	if afterSettle < 0 {
		t.Errorf("bucket went negative after settle: %v", afterSettle)
	}
}

// TestLimitsForGlobalDefaultDisabled proves the disabled global-default path
// (the router returns Limits{} when the operator has not enabled global
// defaults) yields no queue: no concurrency from the fallback means nothing to
// wait for regardless of the key's own columns.
func TestLimitsForGlobalDefaultDisabled(t *testing.T) {
	disabled := func() Limits { return Limits{} }

	// A key with no columns of its own: nothing applies.
	l := limitsFor(&models.APIKey{ID: "k"}, disabled, nil)
	if l.Concurrency != 0 || l.QueueTimeout != 0 {
		t.Errorf("disabled defaults produced %+v, want zero limits", l)
	}

	// A key with its own concurrency cap still gets the documented default even
	// with global defaults off.
	perKey := limitsFor(&models.APIKey{ID: "k", RateLimitConcurrency: new(2)}, disabled, nil)
	if perKey.QueueTimeout != DefaultQueueTimeout {
		t.Errorf("per-key capped QueueTimeout = %v, want %v", perKey.QueueTimeout, DefaultQueueTimeout)
	}
}

// TestLimitsForPerKeyOverridesGlobal proves a key's own values always win over
// the global fallback, including the queue timeout: a global default cannot
// raise a per-key cap, and a per-key cap cannot be lowered by a global one.
func TestLimitsForPerKeyOverridesGlobal(t *testing.T) {
	global := func() Limits { return Limits{RPM: 10, TPM: 100, Concurrency: 2} }

	// Key raises every axis and sets its own queue timeout.
	l := limitsFor(&models.APIKey{
		ID:                   "k",
		RateLimitRPM:         new(50),
		RateLimitTPM:         new(500),
		RateLimitConcurrency: new(8),
		QueueTimeoutMs:       new(250),
	}, global, nil)
	if l.RPM != 50 || l.TPM != 500 || l.Concurrency != 8 {
		t.Errorf("per-key limits did not win: %+v", l)
	}
	if l.QueueTimeout != 250*time.Millisecond {
		t.Errorf("QueueTimeout = %v, want 250ms (per-key)", l.QueueTimeout)
	}
}

// TestMiddlewareRefundsTPMOnQueueRefusal drives the ACTUAL middleware path that
// the reviewer's item #3 is about: TPM is charged before Acquire, the queue
// then refuses, and the middleware must refund the charge. The earlier
// TestRefundTPMReturnsQueueRefusalCharge called RefundTPM directly, so it would
// still pass if the middleware forgot to call it — this one would not.
func TestMiddlewareRefundsTPMOnQueueRefusal(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	const key = "mw-refund"

	handler := RequireRateLimit(rl, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	mkReq := func(content string) *http.Request {
		body := `{"model":"gpt-4o","messages":[{"role":"user","content":"` + content + `"}]}`
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		return req.WithContext(withAPIKey(req, &models.APIKey{
			ID:                   key,
			RateLimitTPM:         new(100000), // ample: TPM never refuses here
			RateLimitConcurrency: new(1),      // one slot
			QueueTimeoutMs:       new(30),     // short queue: the 2nd times out
		}))
	}

	// Hold the single concurrency slot with a handler that blocks until released.
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	blocking := RequireRateLimit(rl, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	go func() {
		rec := httptest.NewRecorder()
		blocking.ServeHTTP(rec, mkReq("holder"))
	}()
	<-entered

	// Snapshot the bucket after the holder's charge.
	rl.mu.Lock()
	afterHolder := rl.tokens[key].tokens
	rl.mu.Unlock()

	// Second request: TPM is charged, then the queue times out (30ms) and the
	// middleware must refund. Content differs so the estimate is the same but
	// the request is distinct.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, mkReq("queued-refused"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second request status = %d, want 429", rec.Code)
	}

	rl.mu.Lock()
	afterRefund := rl.tokens[key].tokens
	rl.mu.Unlock()

	// The refused request must have left the bucket where the holder left it
	// (within refill tolerance): its own charge was returned. If the middleware
	// forgot to refund, the bucket would be lower by that request's estimate.
	if afterRefund < afterHolder-1 {
		t.Errorf("bucket after a queue-refused request = %v, want >= %v (the TPM charge must be refunded)", afterRefund, afterHolder)
	}

	close(release)
}

// TestMiddlewareDoesNotRefundOnTPMRefusal proves the refund is specific to a
// QUEUE refusal. When TPM itself refuses, no charge was taken (checkTPM returns
// false before subtracting), so a refund there would inject tokens that were
// never spent and let a flood replenish the bucket it is meant to drain.
//
// The bucket is first drained to a level below the cap, so a spurious refund is
// observable as a rise above that level rather than being clipped at the limit.
func TestMiddlewareDoesNotRefundOnTPMRefusal(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	const key = "mw-tpm-refuse"

	// Drain a 1000-token bucket to ~100 with charges that succeed.
	for i := 0; i < 9; i++ {
		if !rl.checkTPM(key, 1000, 100, time.Now()) {
			t.Fatalf("drain charge %d refused", i)
		}
	}
	rl.mu.Lock()
	before := rl.tokens[key].tokens
	rl.mu.Unlock()

	handler := RequireRateLimit(rl, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// A prompt sized near the full bucket cannot fit in the ~100 left, so it is
	// refused at the TPM step (not the queue: no concurrency cap, no queue).
	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"` + strings.Repeat("x", 2000) + `"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req = req.WithContext(withAPIKey(req, &models.APIKey{
		ID:           key,
		RateLimitTPM: new(1000),
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (TPM refusal)", rec.Code)
	}

	rl.mu.Lock()
	after := rl.tokens[key].tokens
	rl.mu.Unlock()
	if after > before+1 {
		t.Errorf("bucket rose from %v to %v after a TPM refusal: a refund ran on a refusal that took no charge", before, after)
	}
}

// TestMiddlewareQueueRefusalLeavesNoTPMCharge is the durable assertion behind
// the reservation placement: a request the queue refuses must end with its TPM
// charge given back. (Whether a reservation briefly lived on a context that is
// then discarded is not observable and not what matters; the bucket balance is.)
func TestMiddlewareQueueRefusalLeavesNoTPMCharge(t *testing.T) {
	// A long window makes the refill rate negligible (tpm/window), so a missed
	// refund shows up as a real drop rather than being refilled away during the
	// test.
	rl := NewRateLimiter(time.Hour)
	const key = "mw-nocharge"

	mkReq := func(content string) *http.Request {
		body := `{"model":"gpt-4o","messages":[{"role":"user","content":"` + content + `"}]}`
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		return req.WithContext(withAPIKey(req, &models.APIKey{
			ID:                   key,
			RateLimitTPM:         new(100000),
			RateLimitConcurrency: new(1),
			QueueTimeoutMs:       new(30),
		}))
	}

	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	blocking := RequireRateLimit(rl, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	go func() {
		rec := httptest.NewRecorder()
		blocking.ServeHTTP(rec, mkReq("holder"))
	}()
	<-entered

	rl.mu.Lock()
	afterHolder := rl.tokens[key].tokens
	rl.mu.Unlock()

	// Five queue-refused requests in a row. Each is charged then refused; if any
	// refund is missed the bucket ratchets down by that request's estimate.
	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		RequireRateLimit(rl, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).
			ServeHTTP(rec, mkReq(strings.Repeat("refused ", 200)))
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("request %d status = %d, want 429", i, rec.Code)
		}
	}

	rl.mu.Lock()
	after := rl.tokens[key].tokens
	rl.mu.Unlock()
	// Allow for a little refill; the point is it must not have dropped by five
	// requests' worth of tokens.
	if after < afterHolder-50 {
		t.Errorf("bucket = %v after 5 queue refusals, want ~%v: charges were not refunded", after, afterHolder)
	}
	close(release)
}
