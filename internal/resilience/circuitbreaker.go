// Package resilience holds provider-level admission control: a circuit breaker
// that stops hammering a provider that is failing, and a per-account semaphore
// that caps concurrent requests so one account cannot trigger a 429 cascade.
//
// Both are in-memory and process-local, mirroring VansRouter's
// open-sse/utils/circuitBreaker.js and open-sse/services/accountSemaphore.js.
// The existing per-connection model locks (db.LockConnectionModel) stay: they
// are per-account and persisted, this layer is per-provider and fast.
package resilience

import (
	"sync"
	"time"
)

// State is a circuit breaker state.
type State string

const (
	// StateClosed passes every request.
	StateClosed State = "CLOSED"
	// StateDegraded passes requests but logs a warning: failures are elevated
	// but the threshold has not been reached.
	StateDegraded State = "DEGRADED"
	// StateOpen short-circuits requests until the reset timeout elapses.
	StateOpen State = "OPEN"
	// StateHalfOpen admits a limited number of probe requests to test recovery.
	StateHalfOpen State = "HALF_OPEN"
)

// providerFailureCodes are the statuses that count toward the breaker.
//
// Only provider-level failures count. 429 is deliberately absent: it is
// per-account rate limiting, and counting it would trip the breaker once a few
// accounts are individually throttled, blocking every healthy account with
// them.
var providerFailureCodes = map[int]bool{
	408: true, 500: true, 502: true, 503: true, 504: true, 520: true, 524: true,
}

// IsProviderFailureStatus reports whether an HTTP status counts as a
// provider-level failure for the breaker.
func IsProviderFailureStatus(status int) bool {
	return providerFailureCodes[status]
}

// BreakerOptions configures a breaker. Zero values fall back to the defaults.
type BreakerOptions struct {
	FailureThreshold int
	ResetTimeout     time.Duration
	HalfOpenRequests int
	// MaxBackoffMultiplier caps the exponential growth of the reset timeout as
	// repeated probe cycles fail. BackoffEscalationCount is how many failed
	// probe cycles it takes to double the timeout.
	MaxBackoffMultiplier   float64
	BackoffEscalationCount int
}

const (
	defaultFailureThreshold       = 5
	defaultResetTimeout           = 30 * time.Second
	defaultHalfOpenRequests       = 1
	defaultMaxBackoffMultiplier   = 16
	defaultBackoffEscalationCount = 3
)

func (o BreakerOptions) withDefaults() BreakerOptions {
	if o.FailureThreshold <= 0 {
		o.FailureThreshold = defaultFailureThreshold
	}
	if o.ResetTimeout <= 0 {
		o.ResetTimeout = defaultResetTimeout
	}
	if o.HalfOpenRequests <= 0 {
		o.HalfOpenRequests = defaultHalfOpenRequests
	}
	if o.MaxBackoffMultiplier <= 0 {
		o.MaxBackoffMultiplier = defaultMaxBackoffMultiplier
	}
	if o.BackoffEscalationCount <= 0 {
		o.BackoffEscalationCount = defaultBackoffEscalationCount
	}
	return o
}

// Breaker is one provider's circuit breaker.
type Breaker struct {
	mu                sync.Mutex
	name              string
	opts              BreakerOptions
	state             State
	failureCount      int
	successCount      int
	openedAt          time.Time
	openProbeCycles   int
	halfOpenRemaining int
	// halfOpenAt is when the last probe was admitted, used to detect a probe
	// that never reported back so the breaker cannot stall in HALF_OPEN.
	halfOpenAt  time.Time
	lastFailure time.Time
	// now is overridable in tests so time can be advanced without sleeping.
	now func() time.Time
}

// degradationThreshold is the failure count at which CLOSED becomes DEGRADED.
// It is 60% of the failure threshold, matching the upstream ratio.
func (b *Breaker) degradationThreshold() int {
	t := b.opts.FailureThreshold * 3 / 5
	if t < 1 {
		t = 1
	}
	return t
}

// State reports the current state.
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// Allow reports whether a request may proceed, transitioning OPEN to HALF_OPEN
// once the (backed-off) reset timeout has elapsed.
func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case StateClosed, StateDegraded:
		return true
	case StateOpen:
		if b.now().Sub(b.openedAt) >= b.effectiveTimeout() {
			b.transition(StateHalfOpen)
			b.halfOpenRemaining--
			return true
		}
		return false
	case StateHalfOpen:
		if b.halfOpenRemaining > 0 {
			b.halfOpenRemaining--
			if b.halfOpenRemaining == 0 {
				b.halfOpenAt = b.now()
			}
			return true
		}
		// The admitted probe never reported an outcome (the caller returned
		// early, or the process stalled mid-request). Without this the breaker
		// would sit in HALF_OPEN forever admitting nothing. Treat the elapsed
		// reset timeout as the probe having failed to prove recovery.
		if b.now().Sub(b.halfOpenAt) >= b.effectiveTimeout() {
			b.openProbeCycles++
			b.transition(StateOpen)
			return false
		}
		return false
	}
	return true
}

// RecordSuccess reports a successful request, closing the breaker after a probe
// succeeds or a degraded provider recovers.
func (b *Breaker) RecordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.successCount++
	switch b.state {
	case StateHalfOpen:
		b.transition(StateClosed)
	case StateDegraded:
		if b.successCount >= b.opts.FailureThreshold {
			b.transition(StateClosed)
		}
	}
}

// RecordFailure reports a failed request. Callers must only pass failures that
// IsProviderFailureStatus accepted, or a transport error.
func (b *Breaker) RecordFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failureCount++
	b.lastFailure = b.now()
	switch b.state {
	case StateHalfOpen:
		// The probe failed: reopen and back off further.
		b.openProbeCycles++
		b.transition(StateOpen)
		return
	case StateOpen:
		return
	}
	if b.failureCount >= b.opts.FailureThreshold {
		b.transition(StateOpen)
	} else if b.failureCount >= b.degradationThreshold() && b.state == StateClosed {
		b.transition(StateDegraded)
	}
}

// transition moves to a new state and applies its bookkeeping. Callers hold mu.
func (b *Breaker) transition(next State) {
	b.state = next
	switch next {
	case StateOpen:
		b.openedAt = b.now()
		b.halfOpenRemaining = 0
	case StateHalfOpen:
		b.halfOpenRemaining = b.opts.HalfOpenRequests
		b.halfOpenAt = b.now()
	case StateClosed:
		b.failureCount = 0
		b.successCount = 0
		b.openProbeCycles = 0
		b.openedAt = time.Time{}
	}
}

// effectiveTimeout is the reset timeout scaled by how many probe cycles have
// already failed, capped at MaxBackoffMultiplier.
func (b *Breaker) effectiveTimeout() time.Duration {
	mult := 1.0
	if b.opts.BackoffEscalationCount > 0 {
		steps := b.openProbeCycles / b.opts.BackoffEscalationCount
		for i := 0; i < steps && mult < b.opts.MaxBackoffMultiplier; i++ {
			mult *= 2
		}
	}
	if mult > b.opts.MaxBackoffMultiplier {
		mult = b.opts.MaxBackoffMultiplier
	}
	return time.Duration(float64(b.opts.ResetTimeout) * mult)
}

// RetryAfter is how long until the breaker admits a probe, or 0 when it is not
// OPEN.
func (b *Breaker) RetryAfter() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state != StateOpen {
		return 0
	}
	remaining := b.effectiveTimeout() - b.now().Sub(b.openedAt)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// Reset forces the breaker closed. Used by the dashboard and tests.
func (b *Breaker) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.transition(StateClosed)
}

// BreakerStatus is a snapshot for the dashboard.
type BreakerStatus struct {
	Name         string `json:"name"`
	State        State  `json:"state"`
	FailureCount int    `json:"failureCount"`
	SuccessCount int    `json:"successCount"`
	RetryAfterMs int64  `json:"retryAfterMs"`
	OpenedAt     string `json:"openedAt,omitempty"`
}

// Status returns a snapshot of the breaker.
func (b *Breaker) Status() BreakerStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := BreakerStatus{
		Name:         b.name,
		State:        b.state,
		FailureCount: b.failureCount,
		SuccessCount: b.successCount,
	}
	if b.state == StateOpen {
		remaining := b.effectiveTimeout() - b.now().Sub(b.openedAt)
		if remaining > 0 {
			st.RetryAfterMs = remaining.Milliseconds()
		}
		st.OpenedAt = b.openedAt.UTC().Format(time.RFC3339)
	}
	return st
}

// registry holds one breaker per provider key. A single mutex guards the map;
// individual breakers lock themselves.
var (
	registryMu sync.Mutex
	registry   = map[string]*Breaker{}
)

// BreakerFor returns the breaker for a provider, creating it on first use with
// the given options. Later calls do not change an existing breaker's options,
// so a provider's threshold stays stable once it has state.
func BreakerFor(provider string, opts BreakerOptions) *Breaker {
	registryMu.Lock()
	defer registryMu.Unlock()
	if b, ok := registry[provider]; ok {
		return b
	}
	b := &Breaker{
		name:  provider,
		opts:  opts.withDefaults(),
		state: StateClosed,
		now:   time.Now,
	}
	registry[provider] = b
	return b
}

// AllBreakerStatuses returns a snapshot of every breaker, for the dashboard.
func AllBreakerStatuses() []BreakerStatus {
	registryMu.Lock()
	breakers := make([]*Breaker, 0, len(registry))
	for _, b := range registry {
		breakers = append(breakers, b)
	}
	registryMu.Unlock()
	out := make([]BreakerStatus, 0, len(breakers))
	for _, b := range breakers {
		out = append(out, b.Status())
	}
	return out
}

// ResetAllBreakers closes every breaker. Used by the dashboard and tests.
func ResetAllBreakers() {
	registryMu.Lock()
	breakers := make([]*Breaker, 0, len(registry))
	for _, b := range registry {
		breakers = append(breakers, b)
	}
	registryMu.Unlock()
	for _, b := range breakers {
		b.Reset()
	}
}
