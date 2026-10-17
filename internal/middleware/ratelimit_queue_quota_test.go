package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"9router/proxy/internal/models"
)

// TestAcquireQueuesUntilSlotFrees proves a request over the concurrency cap
// waits for a running request to finish instead of being refused outright —
// the queue behaviour a configured queueTimeoutMs buys.
func TestAcquireQueuesUntilSlotFrees(t *testing.T) {
	rl := NewRateLimiter(time.Minute)

	// Fill the single slot.
	if _, ok := rl.Acquire(context.Background(), "k", 0, 1, 0); !ok {
		t.Fatal("first acquire should win the only slot")
	}

	// A second acquire with a generous timeout must park, then succeed once the
	// first is released.
	done := make(chan bool, 1)
	go func() {
		_, ok := rl.Acquire(context.Background(), "k", 0, 1, 2*time.Second)
		done <- ok
	}()

	// Give the waiter a moment to park, then free the slot.
	time.Sleep(20 * time.Millisecond)
	rl.ReleaseConcurrency("k")

	select {
	case ok := <-done:
		if !ok {
			t.Error("queued acquire failed after the slot was released")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued acquire never returned")
	}
}

// TestAcquireTimesOutWhenNoSlotFrees proves a bounded wait is actually bounded:
// with no release coming, the request is refused rather than held forever.
func TestAcquireTimesOutWhenNoSlotFrees(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	if _, ok := rl.Acquire(context.Background(), "k", 0, 1, 0); !ok {
		t.Fatal("first acquire should win the only slot")
	}

	start := time.Now()
	_, ok := rl.Acquire(context.Background(), "k", 0, 1, 60*time.Millisecond)
	elapsed := time.Since(start)

	if ok {
		t.Error("acquire succeeded with no free slot")
	}
	if elapsed < 50*time.Millisecond {
		t.Errorf("acquire returned in %v, before its own timeout", elapsed)
	}
	if elapsed > time.Second {
		t.Errorf("acquire waited %v, well past its 60ms timeout", elapsed)
	}
}

// TestAcquireZeroTimeoutIsImmediate preserves the pre-queue behaviour: a key
// with no queueTimeoutMs configured is refused at once, exactly as before.
func TestAcquireZeroTimeoutIsImmediate(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	if _, ok := rl.Acquire(context.Background(), "k", 0, 1, 0); !ok {
		t.Fatal("first acquire should win the only slot")
	}

	start := time.Now()
	if _, ok := rl.Acquire(context.Background(), "k", 0, 1, 0); ok {
		t.Error("second acquire should not have succeeded")
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Errorf("zero-timeout acquire blocked for %v", elapsed)
	}
}

// TestAcquireUnlimitedSkipsQueue proves a key with no concurrency cap never
// queues and never blocks, regardless of the timeout.
func TestAcquireUnlimitedSkipsQueue(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	for i := 0; i < 50; i++ {
		if _, ok := rl.Acquire(context.Background(), "k", 0, 0, 5*time.Second); !ok {
			t.Fatalf("acquire %d failed despite no concurrency cap", i)
		}
	}
}

// TestRequireRateLimitQueuesConcurrentRequests drives the middleware end to end:
// two requests against a cap of one, the first holding the slot while the
// second waits, must both succeed once the first finishes.
func TestRequireRateLimitQueuesConcurrentRequests(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	release := make(chan struct{})
	started := make(chan struct{}, 2)

	handler := RequireRateLimit(rl, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	}))

	mkReq := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		return req.WithContext(withAPIKey(req, &models.APIKey{
			ID:                   "kq",
			RateLimitConcurrency: new(1),
			QueueTimeoutMs:       new(3000),
		}))
	}

	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, mkReq())
			codes[idx] = rec.Code
		}(i)
	}

	// Only the first request should reach the handler while the slot is held.
	<-started
	select {
	case <-started:
		t.Fatal("second request reached the handler while the first held the only slot")
	case <-time.After(50 * time.Millisecond):
	}

	// Now let both through, one after the other.
	go func() { close(release) }()
	wg.Wait()

	for i, code := range codes {
		if code != http.StatusOK {
			t.Errorf("request %d status = %d, want 200", i, code)
		}
	}
}

// TestRequireRateLimitQueueTimeoutReturns429 proves a request that cannot get a
// slot in time is refused with 429 and a Retry-After, not held forever.
func TestRequireRateLimitQueueTimeoutReturns429(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
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
			ID:                   "kb",
			RateLimitConcurrency: new(1),
			QueueTimeoutMs:       new(30),
		}))
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, mkReq())
	}()
	<-entered // the first request holds the slot

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, mkReq())
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 is missing a Retry-After header")
	}
	close(block)
	<-done
}

// stubUsage is a TokenUsageReader with a fixed reading.
type stubUsage struct {
	tokens int
	err    error
}

func (s stubUsage) GetApiKeyTokenUsage(string) (int, error) { return s.tokens, s.err }

// TestTokenQuotaRefusesExhaustedKey proves the lifetime budget refuses with 403
// once the key's cumulative usage reaches the quota.
func TestTokenQuotaRefusesExhaustedKey(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	usage := stubUsage{tokens: 5000}
	handler := RequireRateLimit(rl, nil, usage)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req = req.WithContext(withAPIKey(req, &models.APIKey{
		ID:         "quota",
		Key:        "sk-abcdefghijklmnop",
		TokenQuota: new(5000),
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if got := rec.Header().Get("X-Token-Quota-Limit"); got != "5000" {
		t.Errorf("X-Token-Quota-Limit = %q, want 5000", got)
	}
	if got := rec.Header().Get("X-Token-Quota-Used"); got != "5000" {
		t.Errorf("X-Token-Quota-Used = %q, want 5000", got)
	}
}

// TestTokenQuotaAllowsKeyUnderBudget proves a key still inside its budget is
// served.
func TestTokenQuotaAllowsKeyUnderBudget(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	usage := stubUsage{tokens: 4999}
	handler := RequireRateLimit(rl, nil, usage)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req = req.WithContext(withAPIKey(req, &models.APIKey{
		ID:         "quota2",
		Key:        "sk-abcdefghijklmnop",
		TokenQuota: new(5000),
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

// TestTokenQuotaFailsOpenOnReadError proves a storage blip does not lock a key
// out of a service it has always been able to call.
func TestTokenQuotaFailsOpenOnReadError(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	usage := stubUsage{err: errors.New("db down")}
	handler := RequireRateLimit(rl, nil, usage)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req = req.WithContext(withAPIKey(req, &models.APIKey{
		ID:         "quota3",
		Key:        "sk-abcdefghijklmnop",
		TokenQuota: new(1),
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (fail open on quota read error)", rec.Code)
	}
}

// TestTokenQuotaUnsetSkipsRead proves a key with no quota never touches the
// usage reader, so the common path pays nothing.
func TestTokenQuotaUnsetSkipsRead(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	counting := &countingUsage{}
	handler := RequireRateLimit(rl, nil, counting)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req = req.WithContext(withAPIKey(req, &models.APIKey{
		ID:  "noquota",
		Key: "sk-abcdefghijklmnop",
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if counting.calls != 0 {
		t.Errorf("usage read %d time(s) for a key with no quota", counting.calls)
	}
}

type countingUsage struct{ calls int }

func (c *countingUsage) GetApiKeyTokenUsage(string) (int, error) {
	c.calls++
	return 0, nil
}

// TestLimitsForResolvesQuotaAndQueue proves the key's own columns are what the
// limiter reads for the new fields.
func TestLimitsForResolvesQuotaAndQueue(t *testing.T) {
	limits := limitsFor(&models.APIKey{
		TokenQuota:     new(1234),
		QueueTimeoutMs: new(2500),
	}, nil, nil)
	if limits.TokenQuota != 1234 {
		t.Errorf("TokenQuota = %d, want 1234", limits.TokenQuota)
	}
	if limits.QueueTimeout != 2500*time.Millisecond {
		t.Errorf("QueueTimeout = %v, want 2.5s", limits.QueueTimeout)
	}
}

// TestLimitsForDefaultQueueTimeout proves a key that sets a concurrency cap but
// no explicit queue timeout gets the upstream-equivalent 60s default, and that
// a key with no concurrency cap never queues at all.
func TestLimitsForDefaultQueueTimeout(t *testing.T) {
	capped := limitsFor(&models.APIKey{RateLimitConcurrency: new(3)}, nil, nil)
	if capped.QueueTimeout != DefaultQueueTimeout {
		t.Errorf("capped QueueTimeout = %v, want %v", capped.QueueTimeout, DefaultQueueTimeout)
	}

	uncapped := limitsFor(&models.APIKey{}, nil, nil)
	if uncapped.QueueTimeout != 0 {
		t.Errorf("uncapped QueueTimeout = %v, want 0", uncapped.QueueTimeout)
	}

	// A key that sets its OWN queue timeout of zero-like absence but has a
	// per-key concurrency cap keeps the documented default.
	explicit := limitsFor(&models.APIKey{RateLimitConcurrency: new(2), QueueTimeoutMs: new(250)}, nil, nil)
	if explicit.QueueTimeout != 250*time.Millisecond {
		t.Errorf("explicit QueueTimeout = %v, want 250ms", explicit.QueueTimeout)
	}
}

// TestGlobalConcurrencyDefaultDoesNotQueue is the regression for the reviewer's
// blocking issue #1: a concurrency cap that arrives ONLY from the operator's
// global fallback must not silently acquire the 60s queue default. On the
// release before this change such a key was refused immediately; giving it a
// minute-long wait is a behaviour change the dashboard cannot show (the key
// reads "unset").
func TestGlobalConcurrencyDefaultDoesNotQueue(t *testing.T) {
	globalOnly := func() Limits { return Limits{Concurrency: 4} }

	// Key with nothing of its own: concurrency is global, so no queue.
	l := limitsFor(&models.APIKey{ID: "k"}, globalOnly, nil)
	if l.Concurrency != 4 {
		t.Fatalf("Concurrency = %d, want 4 from the global fallback", l.Concurrency)
	}
	if l.QueueTimeout != 0 {
		t.Errorf("QueueTimeout = %v for a global-only concurrency cap, want 0 (immediate refusal)", l.QueueTimeout)
	}

	// The same key, but with its own queue timeout: the operator opted in, so it
	// applies even though the cap is global.
	optedIn := limitsFor(&models.APIKey{ID: "k", QueueTimeoutMs: new(500)}, globalOnly, nil)
	if optedIn.QueueTimeout != 500*time.Millisecond {
		t.Errorf("opt-in QueueTimeout = %v, want 500ms", optedIn.QueueTimeout)
	}

	// The same key, but with its own concurrency cap: the documented default
	// applies even without a global fallback.
	perKey := limitsFor(&models.APIKey{ID: "k", RateLimitConcurrency: new(4)}, nil, nil)
	if perKey.QueueTimeout != DefaultQueueTimeout {
		t.Errorf("per-key QueueTimeout = %v, want %v", perKey.QueueTimeout, DefaultQueueTimeout)
	}
}

// TestRateLimitScopeLabelsTruthfully guards the metric label: a refusal must
// report the axis that actually tripped. Inferring the scope from the Limits
// mislabelled a concurrency-queue timeout as "rpm" whenever the key also had
// an RPM cap, hiding queue pressure behind the wrong series.
func TestRateLimitScopeLabelsTruthfully(t *testing.T) {
	// A queue timeout on a key that also has an RPM cap must label concurrency.
	if got := rateLimitScope("concurrency"); got != "concurrency" {
		t.Errorf("scope concurrency = %q, want concurrency", got)
	}
	if got := rateLimitScope("rpm"); got != "rpm" {
		t.Errorf("scope rpm = %q, want rpm", got)
	}
	if got := rateLimitScope("tpm"); got != "tpm" {
		t.Errorf("scope tpm = %q, want tpm", got)
	}
	if got := rateLimitScope("token_quota"); got != "token_quota" {
		t.Errorf("scope token_quota = %q, want token_quota", got)
	}
	// An unknown label must not become a new metric series.
	if got := rateLimitScope("something-else"); got != "rpm" {
		t.Errorf("unknown scope = %q, want the rpm fallback", got)
	}
}

// TestCheckReturnsRefusedScope proves Check names the axis it refused on, which
// is what the caller forwards to the metric label.
func TestCheckReturnsRefusedScope(t *testing.T) {
	rl := NewRateLimiter(time.Minute)

	// RPM: a one-request window refuses the second.
	allowed, _, scope := rl.Check("c1", 1, 0, 0, 0)
	if !allowed {
		t.Fatal("first request should be allowed")
	}
	allowed, _, scope = rl.Check("c1", 1, 0, 0, 0)
	if allowed {
		t.Fatal("second request should be refused by RPM")
	}
	if scope != "rpm" {
		t.Errorf("scope = %q, want rpm", scope)
	}

	// Concurrency when explicitly asked for (the helper still supports it).
	if _, ok := rl.Acquire(context.Background(), "c2", 0, 1, 0); !ok {
		t.Fatal("acquire should win the only slot")
	}
	allowed, _, scope = rl.Check("c2", 0, 0, 1, 0)
	if allowed {
		t.Fatal("request should be refused by concurrency")
	}
	if scope != "concurrency" {
		t.Errorf("scope = %q, want concurrency", scope)
	}
}

// TestAcquireNeverExceedsCapUnderLoad is the core safety property: no matter
// how many requests race for slots, the number holding one at any instant must
// never exceed the cap. It drives the low-level Acquire/Release pair the
// middleware uses, so a regression in the counter or the wake logic shows up
// here as an observed overflow rather than as a production surge.
func TestAcquireNeverExceedsCapUnderLoad(t *testing.T) {
	const (
		cap      = 4
		workers  = 64
		maxHold  = 5 * time.Millisecond
		deadline = 20 * time.Second
	)
	rl := NewRateLimiter(time.Minute)

	var inFlight int64 // current holders
	var peak int64     // high-water mark observed
	var acquired int64

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok := rl.Acquire(context.Background(), "stress", 0, cap, deadline)
			if !ok {
				t.Error("acquire timed out under load despite a generous deadline")
				return
			}
			now := atomic.AddInt64(&inFlight, 1)
			atomic.AddInt64(&acquired, 1)
			for {
				old := atomic.LoadInt64(&peak)
				if now <= old || atomic.CompareAndSwapInt64(&peak, old, now) {
					break
				}
			}
			if now > cap {
				t.Errorf("in-flight = %d, exceeds cap %d", now, cap)
			}
			time.Sleep(time.Duration(1+int64(i)%int64(maxHold/time.Millisecond)) * time.Millisecond)
			atomic.AddInt64(&inFlight, -1)
			rl.ReleaseConcurrency("stress")
		}()
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(deadline):
		t.Fatal("stress run did not finish; a slot was likely lost")
	}

	if got := atomic.LoadInt64(&acquired); got != workers {
		t.Errorf("acquired = %d, want %d (a slot was lost)", got, workers)
	}
	if p := atomic.LoadInt64(&peak); p > cap {
		t.Errorf("peak in-flight = %d, exceeds cap %d", p, cap)
	}
	if final := atomic.LoadInt64(&inFlight); final != 0 {
		t.Errorf("in-flight after drain = %d, want 0", final)
	}
}

// TestRequireRateLimitQueuesOnRPMLimit proves the middleware waits for the RPM
// window to roll when a request exceeds requests-per-minute — the queue covers
// RPM, not only concurrency. The limiter's window is short so the wait is
// observable without sleeping a minute.
func TestRequireRateLimitQueuesOnRPMLimit(t *testing.T) {
	rl := NewRateLimiter(150 * time.Millisecond)
	handler := RequireRateLimit(rl, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	mkReq := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		return req.WithContext(withAPIKey(req, &models.APIKey{
			ID:             "rpmq",
			RateLimitRPM:   new(1),
			QueueTimeoutMs: new(2000),
		}))
	}

	// First request consumes the single RPM slot.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, mkReq())
	if rec.Code != http.StatusOK {
		t.Fatalf("first request = %d, want 200", rec.Code)
	}

	// Second must wait for the window then succeed, rather than 429 now.
	start := time.Now()
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, mkReq())
	elapsed := time.Since(start)
	if rec.Code != http.StatusOK {
		t.Fatalf("second request = %d, want 200 (queued for the RPM window)", rec.Code)
	}
	if elapsed < 50*time.Millisecond {
		t.Errorf("second request returned in %v; it should have waited for the window", elapsed)
	}
}

// TestRequireRateLimitRPMQueueTimeout429 proves the RPM queue is bounded: with a
// full 60s window and a short queue timeout, the request is refused with 429.
func TestRequireRateLimitRPMQueueTimeout429(t *testing.T) {
	rl := NewRateLimiter(time.Minute)
	handler := RequireRateLimit(rl, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	mkReq := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		return req.WithContext(withAPIKey(req, &models.APIKey{
			ID:             "rpmt",
			RateLimitRPM:   new(1),
			QueueTimeoutMs: new(30),
		}))
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, mkReq())
	if rec.Code != http.StatusOK {
		t.Fatalf("first request = %d, want 200", rec.Code)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, mkReq())
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("second request = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 missing Retry-After")
	}
}
