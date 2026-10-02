package executor

import (
	"sync"
	"time"
)

// PendingCompletionFlushTimeout bounds how long a translated stream may wait
// for the usage trailer its deferred terminal event needs. Mirrors upstream's
// PENDING_COMPLETION_FLUSH_MS (decolua/9router fbcaa282 + 7111db35); a package
// var so a test can drive the bound instead of sleeping through it, and
// exported because the forwarder has to size the upstream read to match.
var PendingCompletionFlushTimeout = 3 * time.Second

// completionWatchdog emits the deferred response.completed when the upstream
// stops sending.
//
// The translator holds the terminal event back when finish_reason arrives
// before the usage trailer (upstream PR #4476), so a client waits for a turn
// that has already ended — and a broken chat upstream that stalls, with no
// trailer and no [DONE] and the connection held open, would keep it waiting
// forever. The watchdog gives that wait an end. The producer's own Close()
// still wins when it arrives first, and sendCompleted stays the single
// emitter, so response.completed can never be written twice.
//
// The timer fires on its own goroutine, so every hop into the bridge is
// serialised on bridge.mu: the watchdog never writes while a producer is
// mid-translation.
type completionWatchdog struct {
	mu      sync.Mutex
	bridge  *ResponsesBridge
	timer   *time.Timer
	emitted bool
}

func newCompletionWatchdog(bridge *ResponsesBridge) *completionWatchdog {
	w := &completionWatchdog{bridge: bridge}
	bridge.watchdog = w
	return w
}

// arm starts the deadline once the terminal event became owed. Restarting a
// live timer rather than stacking new ones keeps a long stream from
// accumulating a timer per chunk.
func (w *completionWatchdog) arm() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.emitted || w.timer != nil {
		return
	}
	w.timer = time.AfterFunc(PendingCompletionFlushTimeout, w.emit)
}

// emit gives up on the trailer and hands the terminal event to the bridge.
func (w *completionWatchdog) emit() {
	w.mu.Lock()
	if w.emitted {
		w.mu.Unlock()
		return
	}
	w.emitted = true
	w.timer = nil
	w.mu.Unlock()

	// The bridge lock is taken here rather than inside feed because the two
	// locks are taken in opposite order elsewhere: feed holds the bridge lock
	// while arming, so holding w.mu across this would be a cycle.
	//
	// Through the bridge, not straight to the writer, so the closed state is
	// flipped too: a producer still draining the upstream must not write a
	// second response.completed after this.
	w.bridge.mu.Lock()
	defer w.bridge.mu.Unlock()
	w.bridge.feed(nil)
}

// stop cancels the deadline. The producer calls it on every path that reaches
// the end of the stream without the watchdog firing.
func (w *completionWatchdog) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
}