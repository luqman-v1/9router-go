package executor

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"

	"9router/proxy/internal/log"
)

// The retirement of upstream chat sessions this provider will not continue.
//
// A browser never deletes a conversation in the same second it created it:
// a delete is something a person does from the sidebar, minutes or hours
// later, several conversations at a time or none. A gateway that opens a
// session per stateless request and closes it the moment the answer is
// streamed produces exactly that create-then-delete-in-the-same-second
// cadence, on every turn, forever — and instant temp-session deletion is one
// of the strongest scripted-client signals the enforcement side can read,
// because no human client has a variance of zero around it.
//
// So with stealth on, a session this provider will not continue is queued
// rather than deleted, and the queue drains the way a person cleaning up
// their sidebar does: in batches of a few sessions, after a delay, with a
// randomized gap between the individual deletes. Every timing here is
// randomized per cycle because a constant interval is itself the signature
// of a scheduler rather than a person.
//
// With stealth off the plain path's immediate delete stands, so the
// comparison mode keeps the shape it always had.

// deepSeekDeleteQueueEntry is one session waiting to be retired.
type deepSeekDeleteQueueEntry struct {
	// turn is the turn that used the session: it carries the client, the
	// browser-fidelity transport and the connection's stealth options, which
	// is everything a later delete needs to look like the turn that created
	// the session.
	turn deepSeekTurn

	userToken string
	sessionID string
}

// deepSeekDeleteQueue holds the sessions waiting to be retired.
//
// One queue serves every account: the entries carry their own credentials,
// and a per-account queue would time out independently for no gain.
type deepSeekDeleteQueue struct {
	mu       sync.Mutex
	pending  []deepSeekDeleteQueueEntry
	timer    *time.Timer
	flushing bool
}

var deepSeekSessionDeletes = &deepSeekDeleteQueue{}

// The batch cadence, as functions so the test suite can drive a flush without
// sitting out the production window. A human cleanup is a few conversations
// at a time (6-10 here), beginning within a couple of minutes of the first
// closed conversation (60-120s), with a short randomized pause between each
// delete (150-450ms).
var (
	deepSeekDeleteBatchSize = func() int { return 6 + rand.IntN(5) }
	deepSeekDeleteWindow    = func() time.Duration {
		return time.Duration(60+rand.IntN(61)) * time.Second
	}
	deepSeekDeleteGap = func() time.Duration {
		return time.Duration(150+rand.IntN(301)) * time.Millisecond
	}
)

// add queues one session for retirement. The batch flushes as soon as it
// reaches the cycle's batch size, or on the cycle's window timer otherwise,
// whichever comes first.
func (q *deepSeekDeleteQueue) add(entry deepSeekDeleteQueueEntry) {
	if entry.sessionID == "" {
		return
	}

	q.mu.Lock()
	q.pending = append(q.pending, entry)
	ready := len(q.pending) >= deepSeekDeleteBatchSize()
	if !ready && q.timer == nil {
		q.timer = time.AfterFunc(deepSeekDeleteWindow(), q.flushAsync)
	}
	q.mu.Unlock()

	if ready {
		q.flushAsync()
	}
}

// flushAsync drains the queue off the caller's path. A delete is never
// something the chat turn waits for.
func (q *deepSeekDeleteQueue) flushAsync() {
	go q.flush(context.Background())
}

// flush retires every queued session it can, re-queuing whatever could not
// be retired yet. A flush already in flight absorbs the request: the entries
// are picked up by the running one.
func (q *deepSeekDeleteQueue) flush(ctx context.Context) {
	q.mu.Lock()
	if q.flushing {
		q.mu.Unlock()
		return
	}
	q.flushing = true
	batch := q.pending
	q.pending = nil
	if q.timer != nil {
		q.timer.Stop()
		q.timer = nil
	}
	q.mu.Unlock()

	var requeued []deepSeekDeleteQueueEntry
	for i, entry := range batch {
		if ctx.Err() != nil {
			requeued = append(requeued, batch[i:]...)
			break
		}
		// The gap runs between deletes, not after the last one: a single
		// delete has no interval to disguise.
		if i > 0 {
			time.Sleep(deepSeekDeleteGap())
		}
		if !q.deleteOne(ctx, entry) {
			// The account's slot is held, which means a generation is in
			// flight: deleting beside it is the very concurrency this queue
			// exists to avoid, so the rest waits for the next window.
			requeued = append(requeued, entry)
		}
	}

	q.mu.Lock()
	q.pending = append(requeued, q.pending...)
	if len(q.pending) > 0 && q.timer == nil {
		q.timer = time.AfterFunc(deepSeekDeleteWindow(), q.flushAsync)
	}
	q.flushing = false
	q.mu.Unlock()
}

// deleteOne retires one session on its own account, under the account's
// concurrency gate. It reports false only when the account is busy, which is
// the caller's signal to try again in the next window; every other failure
// is terminal for the entry, because a delete this upstream already refused
// or a credential that no longer exchanges will not start working by being
// retried.
func (q *deepSeekDeleteQueue) deleteOne(ctx context.Context, entry deepSeekDeleteQueueEntry) bool {
	release, err := deepSeekGates.acquire(ctx, entry.userToken, entry.turn.stealth.maxConcurrency, deepSeekStealthQueueWait)
	if err != nil {
		return false
	}
	defer release()

	callCtx, cancel := context.WithTimeout(ctx, deepSeekDeleteCallTimeout)
	defer cancel()

	accessToken, err := entry.turn.acquireAccessToken(callCtx, entry.userToken)
	if err != nil {
		log.Debug("deepseek-web", "queued session delete skipped: no access token", "error", err)
		return true
	}
	entry.turn.deleteSession(callCtx, accessToken, entry.sessionID)
	return true
}

// deepSeekDeleteCallTimeout bounds one queued delete, token exchange
// included. A cleanup call must never hold the account's slot for long.
const deepSeekDeleteCallTimeout = 30 * time.Second

// deepSeekCloseSession retires a session this turn will not continue: queued
// and batched with stealth on, closed immediately on the plain path.
func deepSeekCloseSession(ctx context.Context, turn deepSeekTurn, userToken, accessToken, sessionID string) {
	if sessionID == "" {
		return
	}
	if turn.stealth.enabled {
		deepSeekSessionDeletes.add(deepSeekDeleteQueueEntry{
			turn:      turn,
			userToken: userToken,
			sessionID: sessionID,
		})
		return
	}
	turn.deleteSession(ctx, accessToken, sessionID)
}

// deepSeekFlushSessionDeletes retires every queued session now, on the
// caller's path. The production queue drains on its own timers; this is the
// synchronous handle for a diagnosis and for the test suite.
func deepSeekFlushSessionDeletes(ctx context.Context) {
	deepSeekSessionDeletes.flush(ctx)
}

// deepSeekResetSessionDeletes drops everything queued and disarms the timer.
func deepSeekResetSessionDeletes() {
	q := deepSeekSessionDeletes
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.timer != nil {
		q.timer.Stop()
		q.timer = nil
	}
	q.pending = nil
}
