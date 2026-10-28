package middleware

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
	"9router/proxy/internal/observ"
	"9router/proxy/internal/policy"
)

// RateLimiter implements per-key rate limiting with sliding window RPM,
// token bucket TPM, and concurrency control, plus a FIFO request queue.
type RateLimiter struct {
	mu      sync.Mutex
	windows map[string]*slidingWindow
	tokens  map[string]*tokenBucket
	active  map[string]*int32
	// queues holds each key's waiting queue. A request that cannot be admitted
	// immediately is parked at the tail and granted at the head, in arrival
	// order, when a slot frees or the RPM window rolls forward — the FIFO
	// fairness the reference's queue provides, so a burst cannot starve an
	// earlier caller.
	queues map[string]*waitQueue
	// windowTimers holds the per-key wake timers that fire when the RPM window
	// rolls far enough to admit the head waiter. One timer per key, removed when
	// it fires.
	windowTimers map[string]*time.Timer
	window       time.Duration
	// maxKeys bounds every per-key map. Eviction prunes them together.
	maxKeys int
	// maxQueueDepth bounds how many requests may park on ONE key. Past it a
	// request is refused rather than queued, so a flood against a saturated
	// key costs a 429, not one goroutine per caller. It is a field, not a
	// constant, so a test can shrink it.
	maxQueueDepth int
	// plans resolves the plan template assigned to a key (F-3). Atomic so a
	// late wiring call cannot race a request in flight; nil means the install
	// has no plan tier, which is every setup that has not configured one.
	plans atomic.Pointer[policy.PlanSource]
}

// waitQueue is the FIFO of parked requests for one key.
type waitQueue struct {
	waiters []*waiter
}

// waiter is one parked request. grant is buffered(1) so a pump can hand over a
// slot without blocking on a caller that has already timed out; the caller
// closes the loop by re-checking whether it still holds the grant.
type waiter struct {
	grant chan struct{}
	// rpm and concurrency are the limits this waiter is queued under. They are
	// captured at enqueue so the pump admits the head against exactly the same
	// limits the caller was refused under, rather than re-deriving them from a
	// key whose configuration may have changed.
	rpm         int
	concurrency int
	// done is set when the waiter gives up (timeout or cancellation), so the
	// pump can discard it without granting.
	done bool
}

type slidingWindow struct {
	stamps []time.Time
}

type tokenBucket struct {
	tokens     float64
	lastRefill time.Time
}

// NewRateLimiter creates a new rate limiter with the given window size.
func NewRateLimiter(window time.Duration) *RateLimiter {
	if window <= 0 {
		window = time.Minute
	}
	return &RateLimiter{
		windows:       make(map[string]*slidingWindow),
		tokens:        make(map[string]*tokenBucket),
		active:        make(map[string]*int32),
		queues:        make(map[string]*waitQueue),
		windowTimers:  make(map[string]*time.Timer),
		window:        window,
		maxKeys:       10000,
		maxQueueDepth: DefaultMaxQueueDepth,
	}
}

// SetPlanSource wires the plan tier (F-3). Call it before the first request;
// the atomic pointer keeps a later rewiring safe for readers already resolving
// a key.
func (rl *RateLimiter) SetPlanSource(src policy.PlanSource) {
	if rl == nil {
		return
	}
	rl.plans.Store(&src)
}

// planSource returns the wired plan source, or nil when none was set.
func (rl *RateLimiter) planSource() policy.PlanSource {
	if rl == nil {
		return nil
	}
	p := rl.plans.Load()
	if p == nil {
		return nil
	}
	return *p
}

// Check verifies if a request is allowed under the given limits.
// Returns (allowed, retryAfterSec, scope), where scope names the axis that
// refused the request ("rpm", "tpm", or "concurrency") and is empty when the
// request was allowed. The scope is returned rather than re-derived so a metric
// label reflects the limit that actually tripped.
//
// Check is the NON-blocking gate. It never queues; a caller that wants to wait
// for a slot uses Acquire, which shares the same admit test.
func (rl *RateLimiter) Check(keyID string, rpm, tpm, concurrency int, promptTokens int) (bool, int, string) {
	// RPM and concurrency share the one admit test Acquire uses, so a key that
	// Check refuses is exactly a key that would have to queue. The scope names
	// which axis tripped, for the metric label.
	if !rl.tryAdmit(keyID, rpm, concurrency) {
		if concurrency > 0 && rl.concurrencyFull(keyID, concurrency) {
			return false, 1, "concurrency"
		}
		return false, rl.rpmRetryAfter(keyID, time.Now()), "rpm"
	}

	// TPM is not part of admission: a token bucket is not queueable, so it is
	// charged (and may refuse) separately after a slot is secured.
	if tpm > 0 {
		now := time.Now()
		if !rl.checkTPM(keyID, tpm, promptTokens, now) {
			return false, rl.tpmRetryAfter(keyID, tpm, promptTokens), "tpm"
		}
	}

	return true, 0, ""
}

// concurrencyFull reports whether the concurrency axis is the one that blocked,
// used only to label a refusal. Must be called with rl.mu held by tryAdmit's
// caller having released it; it re-takes the lock for a consistent read.
func (rl *RateLimiter) concurrencyFull(keyID string, limit int) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	counter, exists := rl.active[keyID]
	return exists && counter != nil && atomic.LoadInt32(counter) >= int32(limit)
}

// ReleaseConcurrency decrements the concurrency counter for a key and then
// advances the queue.
//
// The decrement happens under the lock, before the pump, so a waiter granted in
// the same critical section observes a counter that already reflects this
// release. Waking first would let the pump grant a slot that does not exist.
func (rl *RateLimiter) ReleaseConcurrency(keyID string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if counter, exists := rl.active[keyID]; exists && counter != nil {
		if atomic.AddInt32(counter, -1) < 0 {
			// Defensive: a double release must not make the count go negative,
			// which would hand out a slot that was never held.
			atomic.StoreInt32(counter, 0)
		}
	}
	rl.pumpLocked(keyID)
}

// Acquire admits a request under the key's RPM and concurrency limits, waiting
// in FIFO order for a slot when the key is saturated. It returns the time spent
// waiting and whether a slot was granted.
//
// This is the queue the reference implements: a request that cannot be admitted
// immediately is parked at the tail and granted at the head, in arrival order,
// when a concurrency slot frees OR the RPM window rolls far enough forward to
// admit it. A zero timeout preserves the pre-queue behaviour (try once, refuse),
// so a key that never configured queueTimeoutMs is unchanged.
//
// ctx is honoured: a caller that hangs up (or a server shutdown) leaves the
// queue without being granted a slot. Without that, a cancelled client would
// still be parked, then dispatched upstream on the next free slot — an upstream
// call billed to nobody, and a concurrency slot released for a result no one
// will read.
//
// TPM is not queueable: unlike an RPM window, a token bucket refills
// continuously, so a waiter could be granted and immediately re-refuse forever.
// Callers size TPM against the body before calling here; a TPM refusal is a 429
// from Check, not a wait.
func (rl *RateLimiter) Acquire(ctx context.Context, keyID string, rpm, concurrency int, timeout time.Duration) (waited time.Duration, ok bool) {
	start := time.Now()

	// Fast path: admit without queueing if both axes have room.
	if rl.tryAdmit(keyID, rpm, concurrency) {
		return 0, true
	}
	if timeout <= 0 {
		return 0, false
	}

	// A caller that is already gone must not be parked at all.
	if ctx != nil {
		select {
		case <-ctx.Done():
			return 0, false
		default:
		}
	}

	w := &waiter{grant: make(chan struct{}, 1), rpm: rpm, concurrency: concurrency}
	deadline := start.Add(timeout)

	// Enqueue and re-check under one lock, so a release between the fast-path
	// failure and the enqueue cannot be lost: either the pump sees us at the
	// head and grants immediately, or it grants on the next free.
	rl.mu.Lock()
	// Depth cap: past it, refuse rather than park another live goroutine. This
	// is the pre-queue behaviour on a key with no queue budget, and it keeps a
	// rate-limit rejection from turning into unbounded memory growth under a
	// flood. A non-positive cap (only reachable from a hand-built RateLimiter)
	// falls back to the default rather than refusing every request, which is
	// what a bare >= comparison against zero would do.
	depth := rl.maxQueueDepth
	if depth <= 0 {
		depth = DefaultMaxQueueDepth
	}
	q := rl.queueForLocked(keyID)
	if len(q.waiters) >= depth {
		rl.mu.Unlock()
		return time.Since(start), false
	}
	q.waiters = append(q.waiters, w)
	rl.pumpLocked(keyID)
	rl.mu.Unlock()

	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()

	// ctxDone is nil when there is no caller context, which disables the case
	// in the select below rather than firing on a nil channel.
	var ctxDone <-chan struct{}
	if ctx != nil {
		ctxDone = ctx.Done()
	}

	select {
	case <-w.grant:
		return time.Since(start), true
	case <-timer.C:
	case <-ctxDone:
	}

	// Timed out or the caller went away. Mark the waiter done and let the pump
	// discard it. A grant may have raced in at the deadline; if it did, we are
	// now holding a slot we will never use, so release it rather than leak it.
	rl.mu.Lock()
	w.done = true
	select {
	case <-w.grant:
		// We were granted a slot as we gave up. Hand the concurrency slot
		// back so the next waiter can have it; the RPM stamp is left spent,
		// which is the safe direction (it can only delay, never over-admit).
		if concurrency > 0 {
			if counter := rl.active[keyID]; counter != nil {
				atomic.AddInt32(counter, -1)
			}
		}
	default:
	}
	rl.pumpLocked(keyID)
	rl.mu.Unlock()
	return time.Since(start), false
}

// tryAdmit atomically claims an RPM stamp and (if capped) a concurrency slot.
// It is all-or-nothing: a request that gets the stamp but not the slot returns
// false with the stamp already given back, so a refused request never consumes
// part of the budget.
func (rl *RateLimiter) tryAdmit(keyID string, rpm, concurrency int) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return rl.tryAdmitLocked(keyID, rpm, concurrency)
}

// tryAdmitLocked is tryAdmit under an already-held lock. It also runs the RPM
// window compaction, so it must be the single place a stamp is added.
func (rl *RateLimiter) tryAdmitLocked(keyID string, rpm, concurrency int) bool {
	now := time.Now()

	// RPM: compact the window and test for room before touching the counter, so
	// a full window costs nothing.
	hasStamp := false
	if rpm > 0 {
		if !rl.windowHasRoomLocked(keyID, rpm, now) {
			return false
		}
		hasStamp = true
	}

	// Concurrency: claim a slot if the key is capped.
	claimedSlot := false
	if concurrency > 0 {
		counter := rl.activeCounterLocked(keyID)
		if atomic.LoadInt32(counter) >= int32(concurrency) {
			return false
		}
		atomic.AddInt32(counter, 1)
		claimedSlot = true
	}

	// Commit: only now that both axes are secured do we consume the stamp. Doing
	// it here rather than above is what makes the decision atomic — a request
	// refused on concurrency leaves the RPM window untouched.
	if hasStamp {
		rl.recordStampLocked(keyID, now)
	}
	_ = claimedSlot
	return true
}

// windowHasRoomLocked reports whether the key's RPM window has room for one more
// request, compacting expired stamps as a side effect.
func (rl *RateLimiter) windowHasRoomLocked(keyID string, rpm int, now time.Time) bool {
	window := rl.windowForLocked(keyID)
	cutoff := now.Add(-rl.window)
	valid := window.stamps[:0]
	for _, t := range window.stamps {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	window.stamps = valid
	return len(window.stamps) < rpm
}

// recordStampLocked appends a request stamp to the key's window.
//
// Eviction runs BEFORE the stamp and is told to spare keyID. The admitting key
// is mid-flight: its window was just created by windowHasRoomLocked and is about
// to receive the stamp this call appends. If the sweep reclaimed it — which it
// could, since an RPM-limited key holds no concurrency slot and may have no
// waiter — the stamp would land in a fresh window and the key would be admitted
// past its own RPM limit at the maxKeys boundary.
func (rl *RateLimiter) recordStampLocked(keyID string, now time.Time) {
	rl.evictIfNeededLocked(keyID)
	window := rl.windowForLocked(keyID)
	window.stamps = append(window.stamps, now)
}

func (rl *RateLimiter) windowForLocked(keyID string) *slidingWindow {
	window, exists := rl.windows[keyID]
	if !exists {
		window = &slidingWindow{}
		rl.windows[keyID] = window
	}
	return window
}

func (rl *RateLimiter) activeCounterLocked(keyID string) *int32 {
	counter, exists := rl.active[keyID]
	if !exists {
		counter = new(int32)
		rl.active[keyID] = counter
	}
	return counter
}

func (rl *RateLimiter) queueForLocked(keyID string) *waitQueue {
	q, exists := rl.queues[keyID]
	if !exists {
		q = &waitQueue{}
		rl.queues[keyID] = q
	}
	return q
}

// pumpLocked grants slots to parked waiters, head first, for as long as the
// key can admit them. It must run under rl.mu and is called whenever a resource
// frees (a concurrency release) or time passes (the RPM window rolling).
func (rl *RateLimiter) pumpLocked(keyID string) {
	q, exists := rl.queues[keyID]
	if !exists {
		return
	}
	now := time.Now()

	for len(q.waiters) > 0 {
		head := q.waiters[0]

		// Discard a caller that already gave up (timed out or cancelled).
		if head.done {
			q.waiters = q.waiters[1:]
			continue
		}

		// Test the head against the limits it was queued under, so a waiter
		// and a fresh caller cannot disagree about whether a slot exists.
		if !rl.tryAdmitLockedForWaiter(keyID, head.rpm, head.concurrency) {
			// The head cannot be admitted; FIFO means nobody behind it may
			// overtake. Wake again only when TIME can help — i.e. when the RPM
			// window is what is blocking. A concurrency block clears on
			// ReleaseConcurrency, which calls back into the pump, so scheduling
			// a timer for it would only spin until the timeout.
			if rl.rpmBlockedLocked(keyID, head.rpm, now) {
				rl.scheduleWindowWakeLocked(keyID, now)
			}
			return
		}

		q.waiters = q.waiters[1:]
		// Non-blocking hand-off: the buffer holds the grant for a caller that
		// is between timeout and re-acquire.
		select {
		case head.grant <- struct{}{}:
		default:
		}
	}

	// Queue drained; drop the empty entry so eviction can reclaim the key.
	if len(q.waiters) == 0 {
		delete(rl.queues, keyID)
	}
}

// rpmBlockedLocked reports whether the RPM window is what is holding the head
// waiter back, as opposed to concurrency. Only an RPM block is time-resolved; a
// concurrency block clears on release, so it needs no timer. Must run under
// rl.mu.
func (rl *RateLimiter) rpmBlockedLocked(keyID string, rpm int, now time.Time) bool {
	if rpm <= 0 {
		return false
	}
	window := rl.windows[keyID]
	if window == nil {
		return false
	}
	cutoff := now.Add(-rl.window)
	live := 0
	for _, t := range window.stamps {
		if t.After(cutoff) {
			live++
		}
	}
	return live >= rpm
}

// tryAdmitLockedForWaiter is tryAdmitLocked for a parked waiter, reusing the
// same admit rules so a waiter and a fresh caller cannot disagree about whether
// a slot exists.
func (rl *RateLimiter) tryAdmitLockedForWaiter(keyID string, rpm, concurrency int) bool {
	return rl.tryAdmitLocked(keyID, rpm, concurrency)
}

// scheduleWindowWakeLocked arranges a pump when the head waiter's RPM wait could
// clear. It is a best-effort timer: a wake that fires early simply re-pumps and
// reschedules. The window timer is keyed and replaced so repeated pumps do not
// pile up timers.
func (rl *RateLimiter) scheduleWindowWakeLocked(keyID string, now time.Time) {
	window := rl.windows[keyID]
	if window == nil || len(window.stamps) == 0 {
		return
	}
	if _, exists := rl.windowTimers[keyID]; exists {
		return
	}
	oldest := window.stamps[0]
	wakeIn := oldest.Add(rl.window).Sub(now)
	if wakeIn <= 0 {
		wakeIn = time.Millisecond
	}
	timer := time.AfterFunc(wakeIn, func() {
		rl.mu.Lock()
		delete(rl.windowTimers, keyID)
		rl.pumpLocked(keyID)
		rl.mu.Unlock()
	})
	rl.windowTimers[keyID] = timer
}

// SettleTokenUsage reconciles a reservation against what the turn actually cost.
//
// The pre-dispatch charge is an estimate read off the request body; the real
// cost is only known once the response has been metered. Without this the bucket
// drifts in one direction forever: an over-estimate permanently starves a key
// that is well inside its budget, and an under-estimate lets it spend past the
// very limit that exists to bound it.
//
// Only an under-estimate is corrected. Refunding the surplus would let a client
// bank credit by over-stating its prompt, which is precisely what a token limit
// has to resist.
func (rl *RateLimiter) SettleTokenUsage(keyID string, tpm, reserved, actual int) {
	if tpm <= 0 || keyID == "" || actual <= reserved {
		return
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()
	bucket, exists := rl.tokens[keyID]
	if !exists {
		return
	}
	bucket.tokens -= float64(actual - reserved)
	if bucket.tokens < 0 {
		bucket.tokens = 0
	}
}

// RefundTPM gives back a token-bucket charge that was taken for a request the
// queue then refused.
//
// This is NOT SettleTokenUsage: settle only corrects an under-estimate and
// never refunds, precisely so a client cannot bank credit by over-stating a
// prompt. Here the request was never dispatched, so the charge is not a spend at
// all and the full amount comes back. The refund is capped at the bucket's own
// limit so a refund can never create more headroom than the key was ever given.
func (rl *RateLimiter) RefundTPM(keyID string, tpm, amount int) {
	if tpm <= 0 || keyID == "" || amount <= 0 {
		return
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	bucket, exists := rl.tokens[keyID]
	if !exists {
		return
	}
	bucket.tokens += float64(amount)
	if bucket.tokens > float64(tpm) {
		bucket.tokens = float64(tpm)
	}
}

func (rl *RateLimiter) rpmRetryAfter(keyID string, now time.Time) int {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	window, exists := rl.windows[keyID]
	if !exists || len(window.stamps) == 0 {
		return 1
	}

	oldest := window.stamps[0]
	retryAfter := rl.window - now.Sub(oldest)
	if retryAfter < 0 {
		retryAfter = 0
	}
	return int(retryAfter.Seconds()) + 1
}

func (rl *RateLimiter) checkTPM(keyID string, tpm int, tokens int, now time.Time) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	bucket, exists := rl.tokens[keyID]
	if !exists {
		bucket = &tokenBucket{
			tokens:     float64(tpm),
			lastRefill: now,
		}
		rl.tokens[keyID] = bucket
	}

	// Refill tokens based on elapsed time
	elapsed := now.Sub(bucket.lastRefill).Seconds()
	if elapsed > 0 {
		refillRate := float64(tpm) / rl.window.Seconds()
		bucket.tokens += elapsed * refillRate
		if bucket.tokens > float64(tpm) {
			bucket.tokens = float64(tpm)
		}
		bucket.lastRefill = now
	}

	if bucket.tokens < float64(tokens) {
		return false
	}

	bucket.tokens -= float64(tokens)
	return true
}

func (rl *RateLimiter) tpmRetryAfter(keyID string, tpm int, tokens int) int {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	bucket, exists := rl.tokens[keyID]
	if !exists {
		return 1
	}

	deficit := float64(tokens) - bucket.tokens
	if deficit <= 0 {
		return 1
	}

	refillRate := float64(tpm) / rl.window.Seconds()
	retryAfter := deficit / refillRate
	return int(retryAfter) + 1
}

// evictIfNeededLocked bounds the per-key state so a fleet that mints a key per
// caller cannot grow the limiter's maps without limit. It uses the RPM window
// map's size as the trigger, because that is the map every rate-limited request
// touches, then drops matching entries from every map the same key could occupy
// (window, TPM bucket, concurrency counter, wake channel).
//
// The one entry it never drops is a key that currently holds a concurrency
// slot: removing its counter or its wake channel would either lose a slot (the
// count restarts at zero while the request is still running) or strand a
// parked waiter. An in-flight key is by definition recent, so skipping it
// cannot defeat the bound — the next call evicts it once it drains.
//
// Must be called with rl.mu held.
func (rl *RateLimiter) evictIfNeededLocked(keep string) {
	if len(rl.windows) < rl.maxKeys {
		return
	}
	target := rl.maxKeys * 9 / 10
	for k := range rl.windows {
		// Never evict the key whose admit is in progress: its window is about
		// to be stamped by the caller, and reclaiming it here would let the
		// stamp land in a fresh window and over-admit.
		if k == keep {
			continue
		}
		if rl.keyBusy(k) {
			continue
		}
		// A key with parked waiters is not idle: dropping its queue or wake
		// timer would strand those callers. They hold no concurrency slot, so
		// keyBusy does not cover them; skip them explicitly.
		if q, ok := rl.queues[k]; ok && len(q.waiters) > 0 {
			continue
		}
		delete(rl.windows, k)
		delete(rl.tokens, k)
		delete(rl.active, k)
		delete(rl.queues, k)
		if timer, ok := rl.windowTimers[k]; ok {
			timer.Stop()
			delete(rl.windowTimers, k)
		}
		if len(rl.windows) <= target {
			break
		}
	}
}

// keyBusy reports whether a key currently holds a concurrency slot. Must be
// called with rl.mu held.
func (rl *RateLimiter) keyBusy(keyID string) bool {
	counter, exists := rl.active[keyID]
	if !exists || counter == nil {
		return false
	}
	return atomic.LoadInt32(counter) > 0
}

// TokenUsageReader reports a key's cumulative prompt+completion tokens, which
// is what a lifetime tokenQuota is measured against. It is a narrow interface
// so the limiter does not bind to the repository and a test can supply a fixed
// number without a database.
type TokenUsageReader interface {
	GetApiKeyTokenUsage(rawKey string) (int, error)
}

// RequireRateLimit creates a middleware that enforces per-key rate limits.
//
// TPM is charged against the request's own prompt tokens, so the body is read
// once here to size it and then handed to the next handler intact. Reading it
// unconditionally would buffer large uploads for every request, so it is only
// read when a TPM limit is actually configured.
//
// Order matters. The lifetime quota is a hard budget refused before anything
// else. TPM is a continuous refill and is NOT queueable, so it is charged and
// may refuse outright. RPM and concurrency are both queueable: a request waits
// in the key's FIFO queue, up to queueTimeoutMs, for the window to roll or a
// concurrent slot to free. A key with no queue timeout configured keeps the
// pre-queue behaviour (refuse immediately), because Acquire tries once when the
// timeout is zero.
func RequireRateLimit(rl *RateLimiter, defaults Defaults, usage TokenUsageReader) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			apiKey := GetAuthenticatedApiKey(r)
			if apiKey == nil {
				next.ServeHTTP(w, r)
				return
			}

			limits := limitsFor(apiKey, defaults, rl.planSource())

			// Lifetime token quota: a hard budget, refused with 403 the same way
			// a not-yet-ported plan-tier cap would be. It is checked before
			// RPM/TPM because an exhausted key must not consume a slot or a
			// request stamp on its way to being told no.
			if quotaCheck(w, r, apiKey, limits, usage) {
				return
			}

			// TPM is charged up front and is not queueable: a token bucket
			// refills continuously, so a queued request could be admitted and
			// immediately re-refused forever. It is checked before the queue so
			// an oversized prompt is refused without waiting at all.
			pendingTPM := 0
			if limits.TPM > 0 {
				pendingTPM = promptTokensForRequest(r)
				if allowed, retryAfter, scope := rl.checkTPMOnly(apiKey.ID, limits.TPM, pendingTPM); !allowed {
					rejectRateLimited(w, apiKey.ID, limits, retryAfter, scope)
					return
				}
			}

			// RPM + concurrency: admit now if there is room, otherwise wait in
			// FIFO order up to the key's queue timeout. The caller's context is
			// passed so a client that hangs up leaves the queue instead of being
			// granted a slot and dispatched upstream for nobody.
			waited, ok := rl.Acquire(r.Context(), apiKey.ID, limits.RPM, limits.Concurrency, limits.QueueTimeout)
			if !ok {
				// The TPM charge was taken before the queue. A request the queue
				// refused never reaches the metering path, so the settle hook can
				// never reconcile it: the charge has to be given back here, or a
				// key under a flood spends its token budget on turns that were
				// never served.
				if pendingTPM > 0 {
					rl.RefundTPM(apiKey.ID, limits.TPM, pendingTPM)
				}
				rejectRateLimited(w, apiKey.ID, limits, queueRetryAfter(limits), queueScope(limits))
				return
			}
			if waited > 0 {
				observ.ObserveRateLimitQueueWait(waited.Seconds())
			}
			if limits.Concurrency > 0 {
				defer rl.ReleaseConcurrency(apiKey.ID)
			}

			// A TPM charge is an estimate; the meter reconciles it once the real
			// usage is known. The reservation travels on the context so the settle
			// hook is only installed for requests that were actually charged, and
			// only now that the request is admitted.
			if limits.TPM > 0 {
				r = r.WithContext(withReservedTokens(r.Context(), pendingTPM))
			}

			next.ServeHTTP(w, r)
		})
	}
}

// Provider-scope admission.
//
// The same limiter serves a second scope. A custom provider is one providerNodes
// row — a single upstream endpoint shared by every connection under it — so what
// it tolerates is a property of that endpoint, not of the key calling it. Rather
// than a second limiter (two copies of the sliding window and the FIFO queue,
// free to drift apart), a provider's slot lives in these same maps under a
// namespaced key.
//
// Precedence is deliberately key-first: the middleware refuses a caller that has
// spent its own allowance before a provider slot is ever considered, so one
// misbehaving caller cannot consume the capacity every other caller depends on.

// ProviderScopeKey namespaces one provider's slot inside the shared limiter maps.
func ProviderScopeKey(providerID string) string { return "prov:" + providerID }

// EstimatePromptTokens exposes the limiter's own estimator, so a provider's TPM
// bucket is charged from the same number a key's is.
func EstimatePromptTokens(body []byte) int { return estimatePromptTokens(body) }

// ProviderRefusal says why one provider attempt was refused.
type ProviderRefusal struct {
	// Axis is the limit that refused: "tpm", "rpm" or "concurrency".
	Axis string
	// RetryAfter is how long to wait before trying this provider again. For a
	// queue timeout it is the wait that just elapsed; for TPM it is how long the
	// bucket needs to refill enough for this request.
	RetryAfter time.Duration
	// Limit is the configured value that refused, for the response body.
	Limit int
}

// ProviderSlot is one admitted provider attempt. Release ends it and must run
// when the attempt ends — success, failure, or failover to another candidate.
type ProviderSlot struct {
	rl       *RateLimiter
	key      string
	tpm      int
	reserved int
	held     bool
}

// Settle reconciles the TPM charge with what the turn actually cost. Only an
// under-estimate is corrected, exactly as for a key: refunding a surplus would
// let a caller bank headroom by over-stating its prompt.
func (s *ProviderSlot) Settle(actual int) {
	if s == nil || s.rl == nil {
		return
	}
	if s.tpm > 0 && s.reserved > 0 {
		s.rl.SettleTokenUsage(s.key, s.tpm, s.reserved, actual)
	}
	s.reserved = 0
}

// Release gives back whatever the attempt still holds: the concurrency slot, and
// any TPM charge the meter never settled. An unsettled charge belongs to an
// attempt that ended before it produced a usage reading — an upstream error, or a
// client that hung up — and keeping it would spend a provider's budget on a turn
// nobody was served.
func (s *ProviderSlot) Release() {
	if s == nil || s.rl == nil {
		return
	}
	if s.held {
		s.rl.ReleaseConcurrency(s.key)
		s.held = false
	}
	if s.reserved > 0 {
		s.rl.RefundTPM(s.key, s.tpm, s.reserved)
		s.reserved = 0
	}
}

// AcquireProvider admits one attempt against a provider's limits.
//
// A nil slot with a nil refusal means the provider declares no limits: the
// attempt proceeds and there is nothing to release, which is why every caller can
// hand the slot to a deferred Release unconditionally.
//
// A refusal is not an error to report to the client yet. It names this candidate
// as full; the caller moves to the next candidate and only answers 429 when none
// is left (docs/provider-limits-plan.md D7).
func (rl *RateLimiter) AcquireProvider(ctx context.Context, providerID string, limits Limits, promptTokens int) (*ProviderSlot, *ProviderRefusal) {
	if rl == nil || providerID == "" {
		return nil, nil
	}
	if limits.RPM <= 0 && limits.TPM <= 0 && limits.Concurrency <= 0 {
		return nil, nil
	}
	key := ProviderScopeKey(providerID)
	slot := &ProviderSlot{rl: rl, key: key, tpm: limits.TPM}

	// TPM first, and not queueable: a token bucket refills continuously, so a
	// request parked for one could be admitted and immediately re-refused.
	if limits.TPM > 0 && promptTokens > 0 {
		if allowed, retryAfterSeconds, axis := rl.checkTPMOnly(key, limits.TPM, promptTokens); !allowed {
			return nil, &ProviderRefusal{
				Axis:       axis,
				Limit:      limits.TPM,
				RetryAfter: time.Duration(retryAfterSeconds) * time.Second,
			}
		}
		slot.reserved = promptTokens
	}

	// RPM and concurrency are both queueable here for the same reason as for a
	// key: the request waits in this provider's FIFO, up to this provider's own
	// timeout, rather than being refused the moment a burst arrives.
	waited, ok := rl.Acquire(ctx, key, limits.RPM, limits.Concurrency, limits.QueueTimeout)
	if !ok {
		// The TPM charge was taken before the queue and the attempt never left,
		// so it is not a spend: hand the whole amount back.
		slot.Release()
		return nil, &ProviderRefusal{
			Axis:       queueScope(limits),
			Limit:      queueLimit(limits),
			RetryAfter: time.Duration(queueRetryAfter(limits)) * time.Second,
		}
	}
	if waited > 0 {
		observ.ObserveRateLimitQueueWaitFor("provider", waited.Seconds())
	}
	slot.held = limits.Concurrency > 0
	return slot, nil
}

// queueLimit names the configured value a queue timeout was waiting on, so the
// refusal can report the number the operator actually set.
func queueLimit(limits Limits) int {
	if limits.Concurrency > 0 {
		return limits.Concurrency
	}
	return limits.RPM
}

// Snapshot reports the live admission state of a scope key: how many requests sit
// in its RPM window and how many hold a concurrency slot right now.
//
// It exists for the debug trail a refused provider attempt writes. From the
// refusal alone, "this endpoint is busy" and "this limit is set absurdly low"
// look identical; these two numbers are what tell them apart, and a queue wait
// (which leaves no trace in the window) is why the concurrency count is reported
// even when RPM is the axis that refused.
//
// Stale stamps are excluded rather than counted: the window's own pruning happens
// on the write path, so a count that trusted the slice would report the peak of
// every window that ever rolled, which is exactly the number that would make an
// operator raise a limit that is already generous.
func (rl *RateLimiter) Snapshot(keyID string) (windowCount, active int) {
	if rl == nil || keyID == "" {
		return 0, 0
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	cutoff := time.Now().Add(-rl.window)
	if w, ok := rl.windows[keyID]; ok {
		for _, stamp := range w.stamps {
			if stamp.After(cutoff) {
				windowCount++
			}
		}
	}
	if counter, ok := rl.active[keyID]; ok && counter != nil {
		active = int(atomic.LoadInt32(counter))
	}
	return windowCount, active
}

// checkTPMOnly charges the token bucket and names the refusal when it is full.
// It is separate from Check so the middleware can run TPM before the queue: a
// TPM refusal must not park the request first.
func (rl *RateLimiter) checkTPMOnly(keyID string, tpm, tokens int) (bool, int, string) {
	now := time.Now()
	if rl.checkTPM(keyID, tpm, tokens, now) {
		return true, 0, ""
	}
	return false, rl.tpmRetryAfter(keyID, tpm, tokens), "tpm"
}

// queueScope names which axis a queue timeout was waiting on, for the metric
// label. When both are capped, concurrency is the likelier cause of a wait, but
// the label is only a hint; the Retry-After carries the actionable value.
func queueScope(limits Limits) string {
	if limits.Concurrency > 0 {
		return "concurrency"
	}
	return "rpm"
}

// quotaCheck refuses a key whose lifetime token budget is spent. It returns
// true when the request was written a 403 and the handler must stop.
//
// A failed quota read allows the request: a storage blip must not lock a key
// out of a service it has always been able to call, which mirrors how a failed
// allowlist read degrades to "allow all". The quota is a spend ceiling, not an
// access grant, so failing open costs at most a delayed enforcement.
func quotaCheck(w http.ResponseWriter, r *http.Request, apiKey *models.APIKey, limits Limits, usage TokenUsageReader) bool {
	if limits.TokenQuota <= 0 || usage == nil || apiKey.Key == "" {
		return false
	}
	used, err := usage.GetApiKeyTokenUsage(apiKey.Key)
	if err != nil {
		log.Warn("rate limit", "token quota read failed, allowing", "key", apiKey.ID, "error", err)
		return false
	}
	if used < limits.TokenQuota {
		return false
	}
	log.Warn("rate limit", "token quota exhausted", "key", apiKey.ID, "used", used, "quota", limits.TokenQuota)
	observ.IncRateLimitReject("token_quota", apiKey.ID)
	rejectQuotaExhausted(w, used, limits.TokenQuota)
	return true
}

// queueRetryAfter is the Retry-After for a request that timed out waiting for a
// concurrency slot. The wait is bounded by the key's own timeout, so that is
// the honest answer: retrying sooner only means parking again for the same span.
func queueRetryAfter(limits Limits) int {
	if limits.QueueTimeout <= 0 {
		return 1
	}
	return int(limits.QueueTimeout.Seconds()) + 1
}

// rejectQuotaExhausted answers a key that has spent its lifetime token budget.
//
// A quota is not a per-window rate, so the answer is a plain 403 with the spend
// it reached: Retry-After and X-RateLimit-Reset would both promise that waiting
// helps, and for a lifetime budget only an operator raising the quota does.
func rejectQuotaExhausted(w http.ResponseWriter, used, quota int) {
	w.Header().Set("X-Token-Quota-Limit", strconv.Itoa(quota))
	w.Header().Set("X-Token-Quota-Used", strconv.Itoa(used))
	handlerutil.WriteJSONError(w, http.StatusForbidden,
		fmt.Sprintf("Token quota exhausted for this API key (%d/%d).", used, quota))
}

// EffectiveTPM returns the TPM limit that applies to a key, resolving the
// key's column against the plan tier and the operator's global default
// exactly as the limiter does. It is a method so it resolves through the same
// plan source the limiter charged against.
//
// The metering path needs the same number the limiter charged: reconciling the
// real usage against a different limit would settle the charge into the wrong
// bucket.
func (rl *RateLimiter) EffectiveTPM(d Defaults, apiKey *models.APIKey) int {
	return limitsFor(apiKey, d, rl.planSource()).TPM
}

// reservedCtxKey carries the TPM charge the limiter took for this request, and
// tokenSettlerKey the hook that reconciles it once the real usage is known.
type reservedCtxKey struct{}

type tokenSettlerKey struct{}

// withReservedTokens records the TPM reservation on the context so the metering
// path can settle it.
func withReservedTokens(ctx context.Context, reserved int) context.Context {
	return context.WithValue(ctx, reservedCtxKey{}, reserved)
}

// ReservedTokensFromContext returns the TPM reservation, or false when no TPM
// limit applied and therefore nothing was charged.
func ReservedTokensFromContext(ctx context.Context) (int, bool) {
	if ctx == nil {
		return 0, false
	}
	v, ok := ctx.Value(reservedCtxKey{}).(int)
	return v, ok
}

// WithTokenSettler installs the hook the metering path calls with the turn's
// real token count.
func WithTokenSettler(ctx context.Context, settle func(actual int)) context.Context {
	if ctx == nil || settle == nil {
		return ctx
	}
	return context.WithValue(ctx, tokenSettlerKey{}, settle)
}

// SettleTokenUsageFromContext reconciles the reservation against actual usage,
// if this request had one. A request that was never charged, or whose handler
// never reached the meter, simply has nothing to settle.
func SettleTokenUsageFromContext(ctx context.Context, actual int) {
	if ctx == nil {
		return
	}
	if settle, ok := ctx.Value(tokenSettlerKey{}).(func(int)); ok {
		settle(actual)
	}
}

// Defaults supplies the operator's global fallback, read fresh per request so
// a settings change takes effect without a restart.
type Defaults func() Limits

// Limits is the effective limit set for one key.
type Limits struct {
	RPM         int
	TPM         int
	Concurrency int
	// TokenQuota caps the key's cumulative prompt+completion tokens. It is
	// enforced before dispatch and is not a per-window rate: it only ever
	// decreases the key's remaining budget.
	TokenQuota int
	// QueueTimeout is how long a request may wait for a free concurrency slot
	// before it is refused instead of served.
	QueueTimeout time.Duration
}

// limitsFor resolves the effective limits for a key.
//
// Precedence, least specific first: the operator's global default, the plan
// assigned to the key, then the key's own column. That ordering is what keeps
// a global default from quietly capping a key that was deliberately given a
// higher budget, and lets a plan cap many keys while a single key still
// escapes its plan without editing the plan for everyone on it.
//
// The plan layer comes from policy.Resolve, the one place that merge happens.
func limitsFor(apiKey *models.APIKey, d Defaults, plans policy.PlanSource) Limits {
	// A nil Defaults means the operator configured no global fallback, which
	// is the same as a fallback of zero. Every install without the settings
	// keys reaches here, so it must not be a nil call.
	var out Limits
	if d != nil {
		out = d()
	}
	// The plan tier arrives with the key's own overrides already folded in, so
	// this one value carries the whole precedence rule — global default, then
	// plan, then key — and the two sources can never be applied in the wrong
	// order by two call sites.
	pol := policy.Resolve(plans, apiKey)
	if pol.RPM > 0 {
		out.RPM = pol.RPM
	}
	if pol.TPM > 0 {
		out.TPM = pol.TPM
	}
	if pol.Concurrency > 0 {
		out.Concurrency = pol.Concurrency
	}
	// The quota is a lifetime budget, so it is never taken from the global
	// fallback and never from a plan: a default token quota would silently cap
	// every key in the install, and a plan's token limit is a per-period
	// budget the budget engine enforces — a different guarantee.
	if pol.TokenQuota > 0 {
		out.TokenQuota = pol.TokenQuota
	}

	// Queue timeout. The default only applies when the CONCURRENCY cap is the
	// key's own or its plan's: a cap that arrived only from the operator's
	// global fallback must not silently acquire a 60s queue. On the release
	// before that change such a key was refused immediately, and turning that
	// into a minute-long wait is a behaviour change the dashboard cannot show
	// (the key reads "unset").
	//
	// So the ordering is deliberate: peek at the key's own columns first.
	keyConc := derefInt(apiKey.RateLimitConcurrency)
	keyQueue := derefInt(apiKey.QueueTimeoutMs)
	switch {
	case keyQueue > 0:
		// The operator set a queue timeout on the key: honour it, and it
		// applies whether the concurrency cap is per-key or from its plan.
		out.QueueTimeout = time.Duration(keyQueue) * time.Millisecond
	case keyConc > 0:
		// The key set its own concurrency cap but no timeout: the documented
		// default applies.
		out.QueueTimeout = DefaultQueueTimeout
	case pol.PlanID != "" && pol.Concurrency > 0:
		// The concurrency cap came from the key's plan. A plan is assigned to
		// this key deliberately, so it queues like the key's own cap rather
		// than behaving like the global fallback. Reaching this branch means
		// the key's own column is 0, so pol.Concurrency is the plan's value.
		out.QueueTimeout = DefaultQueueTimeout
	case out.Concurrency > 0:
		// Concurrency came only from the global fallback: no queueing. This is
		// the pre-change behaviour for such a key, and it is the one reading
		// that keeps a global default from changing an existing install.
		out.QueueTimeout = 0
	}
	return out
}

// DefaultMaxQueueDepth is how many requests may wait on one key. It bounds the
// memory a saturated key can hold: each parked request is a live goroutine with
// its parsed body, so an unbounded queue turns a rate-limit rejection into
// unbounded growth. Chosen well above any real concurrency ceiling so it never
// refuses a legitimate burst, but low enough to cap a flood.
const DefaultMaxQueueDepth = 1024

// DefaultQueueTimeout is how long a request waits for a free concurrency slot
// when the key sets its own concurrency cap but no queueTimeoutMs. It matches
// the upstream reference's own 60s fallback, so a key that only sets a
// concurrency cap behaves the same on both gateways rather than being refused
// on the first collision here. It is NOT applied when the concurrency cap came
// from the operator's global fallback: see limitsFor.
const DefaultQueueTimeout = 60 * time.Second

func derefInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

// rejectRateLimited answers a refused request.
//
// The headers name which limit was hit rather than a single X-RateLimit-Limit,
// because a key can be bounded on three independent axes at once and a client
// that has to guess cannot back off correctly. X-RateLimit-Limit still carries
// RPM for the clients that only read the standard name.
func rejectRateLimited(w http.ResponseWriter, keyID string, limits Limits, retryAfter int, scope string) {
	log.Warn("rate limit", "key", keyID, "retryAfter", retryAfter, "scope", scope)
	observ.IncRateLimitReject(rateLimitScope(scope), keyID)

	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	if limits.RPM > 0 {
		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(limits.RPM))
		w.Header().Set("X-RateLimit-Limit-RPM", strconv.Itoa(limits.RPM))
	}
	if limits.TPM > 0 {
		w.Header().Set("X-RateLimit-Limit-TPM", strconv.Itoa(limits.TPM))
	}
	if limits.Concurrency > 0 {
		w.Header().Set("X-RateLimit-Limit-Concurrency", strconv.Itoa(limits.Concurrency))
	}
	w.Header().Set("X-RateLimit-Remaining", "0")
	w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Duration(retryAfter)*time.Second).Unix(), 10))

	handlerutil.WriteJSONError(w, http.StatusTooManyRequests,
		fmt.Sprintf("Rate limit exceeded. Retry after %ds.", retryAfter))
}

// rateLimitScope names which axis refused the request, for the metric label.
//
// The label is a closed vocabulary of five values, so a key id can never become
// a metric cardinality explosion. The caller passes the axis it actually
// refused on: inferring it from the Limits here mislabelled a concurrency-queue
// timeout as "rpm" whenever the key also had an RPM cap, which is exactly the
// common case and would have hidden queue pressure behind the wrong series.
func rateLimitScope(scope string) string {
	switch scope {
	case "rpm", "tpm", "concurrency", "token_quota":
		return scope
	default:
		return "rpm"
	}
}

// maxTokenScanBody caps how much of a body the limiter buffers to size a TPM
// charge. A prompt past this is charged from its declared length instead of
// being read, so a large multimodal or base64 payload cannot make every
// request hold two copies of itself in memory.
const maxTokenScanBody = 4 << 20 // 4 MiB

// promptTokensForRequest sizes the prompt for the TPM charge, leaving r.Body
// readable for the next handler.
func promptTokensForRequest(r *http.Request) int {
	if r.Body == nil || r.Body == http.NoBody {
		return 0
	}
	// A body too large to buffer is charged from the declared length, which
	// over-counts relative to a real tokenizer. Biasing high is the safe
	// direction for a limit: it can reject a request that would have fit,
	// never admit one that would not.
	if r.ContentLength > maxTokenScanBody {
		return int(r.ContentLength/4) + 1
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxTokenScanBody))
	if err != nil {
		// The body is consumed on error and cannot be handed on. Charging the
		// request and letting the handler see a closed body would turn a
		// limiter problem into a request failure, so the limiter declines to
		// judge it.
		return 0
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return estimatePromptTokens(body)
}
