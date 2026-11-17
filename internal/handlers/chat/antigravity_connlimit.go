package chat

import (
	"context"
	"errors"
	"net/http"
	"time"

	"9router/proxy/internal/connlimit"
	"9router/proxy/internal/proxy"
	"sync"
)

// antigravitySerial bounds in-flight upstream calls per connection to one, the
// way the Antigravity IDE issues them.
//
// The backend refuses the fourth concurrent call for one account (probed
// 2026-11-17 on a live consumer account: two at once answered 200/200, four at
// once answered 200/200/200/429 RESOURCE_EXHAUSTED, while the same calls in
// sequence never failed). A 429 there parks the account for every model on it —
// argon included — and three in a row open the breaker for a minute, so a
// client that fans out parallel tool calls used to take the whole account down
// and then report "carbon does not work". Extra calls now wait their turn, the
// way the IDE's own single request does.
const (
	// antigravityQueueDepth bounds how many calls may wait for the one
	// in-flight slot. connlimit's contract is "fail fast, no unbounded
	// queueing", and a client that fans out thousands of calls must not turn
	// the serial slot into an unbounded backlog.
	antigravityQueueDepth = 8
	// antigravityQueueWait bounds a single wait, so a stuck upstream releases
	// its waiters instead of parking them for the life of the process.
	antigravityQueueWait = 2 * time.Minute
)

var antigravitySerial = struct {
	mu sync.Mutex
	m  map[string]*antigravityQueue
}{m: make(map[string]*antigravityQueue)}

type antigravityQueue struct {
	slot    chan struct{}
	waiting int
}

// errAntigravityQueueFull reports a call refused because the queue behind the
// single slot is already at antigravityQueueDepth.
var errAntigravityQueueFull = errors.New("antigravity connection queue is full")

func acquireAntigravitySlot(ctx context.Context, connectionID string) (func(), error) {
	antigravitySerial.mu.Lock()
	q, ok := antigravitySerial.m[connectionID]
	if !ok {
		q = &antigravityQueue{slot: make(chan struct{}, 1)}
		antigravitySerial.m[connectionID] = q
	}
	if q.waiting >= antigravityQueueDepth {
		antigravitySerial.mu.Unlock()
		return nil, errAntigravityQueueFull
	}
	q.waiting++
	antigravitySerial.mu.Unlock()

	drop := func() {
		antigravitySerial.mu.Lock()
		q.waiting--
		antigravitySerial.mu.Unlock()
	}
	timer := time.NewTimer(antigravityQueueWait)
	defer timer.Stop()
	select {
	case q.slot <- struct{}{}:
		drop()
		return func() { <-q.slot }, nil
	case <-timer.C:
		drop()
		return nil, context.DeadlineExceeded
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	}
}

// admitAntigravity bounds per-connection concurrency and enforces backoff and
// the circuit breaker before an Antigravity upstream call.
//
// Steps, in order:
//  1. connlimit.Allow: breaker open → fail fast (zero upstream calls).
//     Backoff window active → sleep for the computed wait, honouring ctx.
//  2. connlimit.Acquire: reserve one in-flight slot; at the limit fail fast
//     with a labelled error (no unbounded queueing).
//
// The caller must invoke the returned release exactly once, on every path,
// including after ForwardGemini returns.
func admitAntigravity(ctx context.Context, connectionID string) (release func(), err error) {
	if connectionID == "" {
		return func() {}, nil
	}
	// The serial slot comes first, and the breaker/backoff check runs while it
	// is held. Two reasons: connlimit's own bound is three, so a fourth
	// parallel call would be refused with 429 before it reached the queue —
	// exactly the burst the backend punishes — and a check made before the
	// slot could let a request proceed on a breaker another waiter opened in
	// the meantime (fail-open). Holding the slot across the backoff sleep is
	// intentional: it is the queue.
	slotRelease, err := acquireAntigravitySlot(ctx, connectionID)
	if err != nil {
		if errors.Is(err, errAntigravityQueueFull) {
			return nil, &proxy.UpstreamError{
				StatusCode: http.StatusTooManyRequests,
				Local:      true,
				Body:       []byte(`{"error":{"message":"antigravity connection queue is full","type":"queue_full","code":429}}`),
			}
		}
		return nil, err
	}
	wait, allowed := connlimit.Allow(connectionID)
	if !allowed {
		slotRelease()
		return nil, &proxy.UpstreamError{
			StatusCode: http.StatusServiceUnavailable,
			Body:       []byte(`{"error":{"message":"antigravity circuit breaker open for connection","type":"circuit_breaker_open","code":503}}`),
		}
	}
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			slotRelease()
			return nil, ctx.Err()
		}
	}
	slot, err := connlimit.Acquire(ctx, connectionID)
	if err != nil {
		slotRelease()
		if errors.Is(err, connlimit.ErrConnLimit) {
			return nil, &proxy.UpstreamError{
				StatusCode: http.StatusTooManyRequests,
				Body:       []byte(err.Error()),
			}
		}
		return nil, err
	}
	// Both handles are copied into the closure on purpose: naming one `release`
	// (the result variable) would make the closure capture that variable, and the
	// return assignment would point it at the closure itself.
	return func() {
		slot()
		slotRelease()
	}, nil
}

// recordAntigravityOutcome charges the attempt's result to the per-connection
// backoff and breaker. A 2xx resets everything (only a real successful response
// releases the breaker — never a token refresh). 403/429 widen the backoff and
// may open the breaker; a 403 TOS_VIOLATION bypasses both entirely (#996).
func recordAntigravityOutcome(connectionID string, status int, body []byte) {
	if connectionID == "" {
		return
	}
	if status >= 200 && status < 300 {
		connlimit.RecordSuccess(connectionID)
		return
	}
	if status == http.StatusForbidden || status == http.StatusTooManyRequests {
		connlimit.RecordFailure(connectionID, status, body)
	}
}

// closeAntigravityState drops a connection's breaker/backoff state. Called on
// connection delete and re-authentication so nothing leaks.
func closeAntigravityState(connectionID string) {
	if connectionID == "" {
		return
	}
	connlimit.Close(connectionID)
}
