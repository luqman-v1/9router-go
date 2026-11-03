package resilience

import (
	"context"
	"time"
)

// Controller is the single entry point the request path uses. It pairs the
// per-provider breaker with the per-account semaphore so a caller does not have
// to know which registry to consult or how to build either key.
//
// The two layers answer different questions and both must pass before a request
// goes out:
//
//   - the breaker asks "is this provider healthy at all?" — provider-scoped, so
//     one dead provider stops every account's traffic to it;
//   - the semaphore asks "may THIS account take another concurrent request?" —
//     account-scoped, so one busy account does not throttle the others.
//
// A caller that only consulted the semaphore would keep queueing requests to a
// provider that is answering 500 to everyone; one that only consulted the
// breaker would let a single account pile up until it earns a 429.
type Controller struct {
	Breaker   BreakerOptions
	Semaphore SemaphoreOptions
}

// NewController returns a Controller with the given options.
func NewController(breaker BreakerOptions, semaphore SemaphoreOptions) *Controller {
	return &Controller{Breaker: breaker, Semaphore: semaphore}
}

// DefaultController returns a Controller with the package defaults.
func DefaultController() *Controller {
	return NewController(BreakerOptions{}, SemaphoreOptions{})
}

// Decision is what Admit reports.
type Decision struct {
	// Allowed is whether the request may proceed to the upstream.
	Allowed bool
	// RetryAfter is how long until the provider's breaker admits a probe. Zero
	// when the breaker is not the reason for the refusal.
	RetryAfter time.Duration
	// Reason names the layer that refused, for logs and the dashboard.
	Reason string
}

// Admit reports whether a request to this provider/account may proceed, without
// reserving anything. Use Reserve to actually take a slot.
func (c *Controller) Admit(provider, accountKey, proxyHash string) Decision {
	b := BreakerFor(provider, c.Breaker)
	if !b.Allow() {
		return Decision{RetryAfter: b.RetryAfter(), Reason: "breaker"}
	}
	return Decision{Allowed: true}
}

// Reserve takes the account's concurrency slot for the duration of the request.
// The caller MUST call the returned release exactly once; it is idempotent.
//
// The breaker is consulted first and the semaphore only entered when the breaker
// passes: queueing behind an account while the provider itself is failing would
// hold requests for a reset timeout that no account slot can shorten.
//
// A breaker refusal returns (nil, false) with the retry hint in the Decision. A
// semaphore refusal (queue full or wait timed out) also returns (nil, false) so
// the caller can fall back to the next connection.
func (c *Controller) Reserve(ctx context.Context, provider, accountKey, proxyHash string) (func(), bool) {
	if d := c.Admit(provider, accountKey, proxyHash); !d.Allowed {
		return nil, false
	}
	release, err := Acquire(ctx, SemaphoreKey(provider, accountKey, proxyHash), c.Semaphore)
	if err != nil {
		// The slot was refused, which is not a provider failure: the breaker
		// must not be charged for this account's queue being full.
		return nil, false
	}
	return release, true
}

// ReportSuccess records a successful upstream call against the provider breaker
// and releases the account slot.
func (c *Controller) ReportSuccess(provider, accountKey, proxyHash string, release func()) {
	if release != nil {
		release()
	}
	BreakerFor(provider, c.Breaker).RecordSuccess()
}

// ReportFailure records a failed upstream call against the provider breaker and
// releases the account slot. status is the HTTP status, or 0 for a transport
// error — both count, because a connection that never produced a response is
// just as dead to the caller as one that answered 500.
//
// A status that is NOT a provider-level failure (401, 429, a plain 400) must not
// be reported here: those say something about the account or the request, and
// charging them to the breaker would trip it for every healthy account.
func (c *Controller) ReportFailure(provider, accountKey, proxyHash string, release func(), status int) {
	if release != nil {
		release()
	}
	if status != 0 && !IsProviderFailureStatus(status) {
		return
	}
	BreakerFor(provider, c.Breaker).RecordFailure()
}

// BlockAccount holds an account's gate for the given duration, used when the
// upstream answers 429 so queued and future requests wait instead of piling on.
func (c *Controller) BlockAccount(provider, accountKey, proxyHash string, d time.Duration) {
	Block(SemaphoreKey(provider, accountKey, proxyHash), d)
}

// BreakerStatus returns the provider's breaker snapshot.
func (c *Controller) BreakerStatus(provider string) BreakerStatus {
	return BreakerFor(provider, c.Breaker).Status()
}

// Reset closes every breaker and clears every gate. Used by the dashboard and
// tests.
func (c *Controller) Reset() {
	ResetAllBreakers()
	ResetSemaphores()
}

// Statuses returns every breaker snapshot plus every semaphore gate, for the
// dashboard's resilience panel.
func (c *Controller) Statuses() ([]BreakerStatus, []SemaphoreStat) {
	return AllBreakerStatuses(), SemaphoreStats()
}
