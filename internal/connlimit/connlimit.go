// Package connlimit provides per-connection admission control for Antigravity:
// a concurrency bound on in-flight upstream requests, exponential backoff with
// jitter on repeated 403/429 failures, and a circuit breaker that fails fast
// until a cool-down elapses and then admits exactly one half-open probe.
//
// All state is keyed by connection ID and held in one process-wide limiter.
// Every per-connection structure has a lifetime and an eviction path: Close
// drops a connection's entry (called on connection delete and re-auth), and
// the registry is capped so it cannot grow without bound.
package connlimit

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"
)

// ErrConnLimit is returned when a connection already has the maximum number of
// in-flight upstream requests. The message names the limit so callers and logs
// can identify it without string matching on the body.
var ErrConnLimit = errors.New("connection concurrency limit reached")

const (
	// DefaultConcurrency is the default max in-flight upstream requests per
	// Antigravity connection. A single interactive turn is one request; light
	// parallel tool use stays under this, while a retry storm is capped at 3
	// concurrent calls instead of an unbounded queue — low enough not to look
	// like automated probing to Google's abuse filters.
	DefaultConcurrency = 3

	// DefaultFailureThreshold is how many consecutive 403/429 failures open the
	// breaker for a connection.
	DefaultFailureThreshold = 3

	// DefaultCoolDown is how long an open breaker stays open before admitting a
	// single half-open probe.
	DefaultCoolDown = 30 * time.Second

	// DefaultBaseBackoff is the base backoff after the first 403/429; it doubles
	// per consecutive failure (2s, 4s, 8s …) up to DefaultMaxBackoff.
	DefaultBaseBackoff = 2 * time.Second

	// DefaultMaxBackoff caps the exponential backoff.
	DefaultMaxBackoff = 1 * time.Minute

	// maxConns bounds the number of tracked connections so the registry cannot
	// grow without bound on client-supplied IDs.
	maxConns = 10000
)

type breakerState int

const (
	breakerClosed breakerState = iota
	breakerOpen
	breakerHalfOpen
)

// connState is the per-connection admission state.
type connState struct {
	mu                sync.Mutex
	sem               chan struct{} // buffered; capacity = concurrency limit
	failures          int           // consecutive 403/429 failures
	backoffUntil      time.Time     // next attempt must not happen before this
	breaker           breakerState
	openedAt          time.Time     // when the breaker opened
	coolDown          time.Duration // breaker cool-down
	halfOpenRemaining int           // probes left in half-open state (always 0/1)
	halfOpenAt        time.Time     // when the single half-open probe was admitted
	lastActivity      time.Time
}

// Limiter is a per-connection admission controller. Zero value is not usable;
// use New.
type Limiter struct {
	mu    sync.Mutex
	conns map[string]*connState

	concurrency      int
	failureThreshold int
	baseBackoff      time.Duration
	maxBackoff       time.Duration
	coolDown         time.Duration
	maxConns         int
	now              func() time.Time
}

// New returns a limiter with the given concurrency and failure threshold;
// remaining fields take defaults.
func New(concurrency, failureThreshold int) *Limiter {
	if concurrency <= 0 {
		concurrency = DefaultConcurrency
	}
	if failureThreshold <= 0 {
		failureThreshold = DefaultFailureThreshold
	}
	return &Limiter{
		conns:            make(map[string]*connState),
		concurrency:      concurrency,
		failureThreshold: failureThreshold,
		baseBackoff:      DefaultBaseBackoff,
		maxBackoff:       DefaultMaxBackoff,
		coolDown:         DefaultCoolDown,
		maxConns:         maxConns,
		now:              time.Now,
	}
}

var (
	defaultMu      sync.Mutex
	defaultLimiter *Limiter
)

func defaultInstance() *Limiter {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	if defaultLimiter == nil {
		defaultLimiter = New(DefaultConcurrency, DefaultFailureThreshold)
	}
	return defaultLimiter
}

// Allow reports whether a new attempt on connID may proceed now, and how long
// the caller must wait before sending. When allowed is false the breaker is
// open and no upstream call should be made; wait is the time until a half-open
// probe is admitted. When allowed is true and wait is positive, the caller is
// in a backoff window and should sleep for wait (honouring its context) before
// the attempt.
func Allow(connID string) (wait time.Duration, allowed bool) {
	return defaultInstance().Allow(connID)
}

// Allow is the method form.
func (l *Limiter) Allow(connID string) (wait time.Duration, allowed bool) {
	l.mu.Lock()
	s := l.getOrCreate(connID)
	l.mu.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastActivity = l.now()
	now := l.now()

	switch s.breaker {
	case breakerOpen:
		if until := s.openedAt.Add(s.coolDown); now.Before(until) {
			return until.Sub(now), false
		}
		// Cool-down elapsed: the transition itself is the single half-open
		// probe. Admit it and mark none remaining so the breaker cannot stay
		// half-open admitting more than one.
		s.breaker = breakerHalfOpen
		s.halfOpenRemaining = 0
		s.halfOpenAt = now
		return 0, true
	case breakerHalfOpen:
		// The single probe was already admitted on the OPEN→HALF_OPEN
		// transition; the state resolves on RecordSuccess/RecordFailure. If a
		// probe never reports back, the cool-down elapsed since it was admitted
		// reopens the breaker so it cannot stall in HALF_OPEN forever.
		if !s.halfOpenAt.IsZero() && now.After(s.halfOpenAt.Add(s.coolDown)) {
			s.breaker = breakerOpen
			s.openedAt = now
			s.halfOpenRemaining = 0
			return s.coolDown, false
		}
		return 0, false
	}

	// breakerClosed: honour any backoff window, otherwise proceed.
	if !s.backoffUntil.IsZero() && now.Before(s.backoffUntil) {
		return s.backoffUntil.Sub(now), true
	}
	return 0, true
}

// Acquire reserves one in-flight slot for connID. It never blocks and never
// spawns a goroutine: when the connection is at its concurrency limit it returns
// a labelled error immediately (fail fast, no unbounded queueing). The caller
// must invoke the returned release exactly once, on every path.
func Acquire(ctx context.Context, connID string) (release func(), err error) {
	return defaultInstance().Acquire(ctx, connID)
}

// Acquire is the method form.
func (l *Limiter) Acquire(ctx context.Context, connID string) (release func(), err error) {
	l.mu.Lock()
	s := l.getOrCreate(connID)
	l.mu.Unlock()

	select {
	case s.sem <- struct{}{}:
		return func() { <-s.sem }, nil
	default:
	}

	return nil, fmt.Errorf("%w: connection %q has %d in-flight upstream requests (limit %d)",
		ErrConnLimit, connID, l.concurrency, l.concurrency)
}

// RecordSuccess resets a connection's backoff, failure count and breaker after
// a successful upstream response. It never clears on a token refresh alone;
// only a real successful response releases the breaker.
func RecordSuccess(connID string) {
	defaultInstance().RecordSuccess(connID)
}

func (l *Limiter) RecordSuccess(connID string) {
	l.mu.Lock()
	s := l.getOrCreate(connID)
	l.mu.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures = 0
	s.backoffUntil = time.Time{}
	s.breaker = breakerClosed
	s.openedAt = time.Time{}
	s.coolDown = 0
	s.halfOpenRemaining = 0
	s.halfOpenAt = time.Time{}
	s.lastActivity = l.now()
}

// RecordFailure charges a 403/429 failure to connID, widening the backoff and,
// at the threshold, opening the breaker. A 403 TOS_VIOLATION bypasses this
// path entirely: it is permanent and handled elsewhere (#996).
func RecordFailure(connID string, status int, body []byte) {
	defaultInstance().RecordFailure(connID, status, body)
}

func (l *Limiter) RecordFailure(connID string, status int, body []byte) {
	if IsTOSViolation(status, body) {
		return // permanent: #996 handles it, never charge backoff or breaker
	}
	if status != http.StatusForbidden && status != http.StatusTooManyRequests {
		return // only 403/429 feed the per-connection backoff and breaker
	}

	l.mu.Lock()
	s := l.getOrCreate(connID)
	l.mu.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures++
	now := l.now()

	// Exponential backoff, capped, plus up to 50% jitter so concurrent
	// connections do not retry in lockstep.
	delay := BackoffDelay(s.failures)
	jitter := time.Duration(rand.Int64N(int64(delay / 2)))
	s.backoffUntil = now.Add(delay + jitter)

	if s.failures >= l.failureThreshold {
		s.breaker = breakerOpen
		s.openedAt = now
		s.coolDown = l.coolDown
		s.halfOpenRemaining = 0
		s.halfOpenAt = time.Time{}
	}
}

// Close drops connID's state, releasing its breaker and backoff. Called on
// connection delete and on re-authentication so nothing leaks.
func Close(connID string) {
	defaultInstance().Close(connID)
}

func (l *Limiter) Close(connID string) {
	l.mu.Lock()
	delete(l.conns, connID)
	l.mu.Unlock()
}

// getOrCreate returns the state for connID, creating it on first use. Callers
// hold l.mu. When the registry is at capacity it evicts the least recently
// active entry so the map cannot grow without bound.
func (l *Limiter) getOrCreate(connID string) *connState {
	if s, ok := l.conns[connID]; ok {
		return s
	}
	if len(l.conns) >= l.maxConns {
		l.evictStale()
	}
	s := &connState{
		sem:          make(chan struct{}, l.concurrency),
		breaker:      breakerClosed,
		lastActivity: l.now(),
	}
	l.conns[connID] = s
	return s
}

func (l *Limiter) evictStale() {
	var (
		victim string
		oldest time.Time
	)
	for id, s := range l.conns {
		s.mu.Lock()
		act := s.lastActivity
		s.mu.Unlock()
		if oldest.IsZero() || act.Before(oldest) {
			oldest = act
			victim = id
		}
	}
	if victim != "" {
		delete(l.conns, victim)
	}
}

// BackoffDelay returns the base (pre-jitter) backoff after the given number of
// consecutive failures: failure 1 → base, 2 → 2×base, … capped at maxBackoff.
// Exported so tests can compute the expected delay without sleeping.
func BackoffDelay(failures int) time.Duration {
	l := defaultInstance()
	return l.backoffDelay(failures)
}

func (l *Limiter) backoffDelay(failures int) time.Duration {
	if failures <= 0 {
		return 0
	}
	delay := l.baseBackoff
	for i := 1; i < failures; i++ {
		delay *= 2
		if delay > l.maxBackoff {
			delay = l.maxBackoff
			break
		}
	}
	return delay
}

// IsTOSViolation reports whether an upstream error is a permanent 403
// TOS_VIOLATION, which must bypass the backoff and breaker paths.
func IsTOSViolation(status int, body []byte) bool {
	if status != http.StatusForbidden || len(body) == 0 {
		return false
	}
	return containsTOSViolation(body)
}

func containsTOSViolation(body []byte) bool {
	return containsFolded(body, "tos_violation") || containsFolded(body, "TOS_VIOLATION")
}

// containsFolded reports whether body contains sub, case-insensitively.
func containsFolded(body []byte, sub string) bool {
	subLen := len(sub)
	if subLen == 0 {
		return true
	}
	lower := make([]byte, subLen)
	for i := 0; i < subLen; i++ {
		c := sub[i]
		if 'A' <= c && c <= 'Z' {
			c += 32
		}
		lower[i] = c
	}
	for i := 0; i+subLen <= len(body); i++ {
		match := true
		for j := 0; j < subLen; j++ {
			c := body[i+j]
			if 'A' <= c && c <= 'Z' {
				c += 32
			}
			if c != lower[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
