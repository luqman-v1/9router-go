package shutdown

import (
	"context"
	"sync"
)

var (
	mu    sync.Mutex
	fired bool
	done  = make(chan struct{})
)

// requested is closed by RequestStop. Unlike Done — which also fires on ^C and
// from the Fx OnStop hook — it means an operator asked this process to exit, so
// main can stop blocking on a signal that will never arrive.
var requested = make(chan struct{})

// Done returns a channel that is closed when shutdown begins.
// In-flight work (SSE streams, upstream readers) selects on it to stop promptly
// instead of holding server.Shutdown until its deadline.
func Done() <-chan struct{} {
	mu.Lock()
	defer mu.Unlock()
	return done
}

// Fired reports whether shutdown has been triggered.
func Fired() bool {
	mu.Lock()
	defer mu.Unlock()
	return fired
}

// ctxMu guards ctx (recreated on TestReset).
var (
	ctxMu        sync.Mutex
	ctx, ctxStop = context.WithCancel(context.Background())
)

// Context returns a context canceled when shutdown begins. Background loops
// (updater, catalog sync) take this instead of context.Background() so they
// exit promptly on ^C instead of leaking goroutines + tickers.
func Context() context.Context {
	ctxMu.Lock()
	defer ctxMu.Unlock()
	return ctx
}

// RequestStop asks this process to exit. main selects on StopRequested next to
// SIGINT/SIGTERM, which is the only portable route on Windows: there
// os.Process.Signal(syscall.SIGTERM) fails with "not supported by windows", so
// a self-signalled stop could never work. Cancel fires first so in-flight
// streams end before the drain begins. Safe to call more than once.
func RequestStop() {
	Cancel()
	mu.Lock()
	defer mu.Unlock()
	select {
	case <-requested:
	default:
		close(requested)
	}
}

// StopRequested returns a channel closed once RequestStop has been called.
func StopRequested() <-chan struct{} {
	mu.Lock()
	defer mu.Unlock()
	return requested
}

// TestReset restores the package for tests (uncancel + unfire).
func TestReset() {
	mu.Lock()
	fired = false
	done = make(chan struct{})
	requested = make(chan struct{})
	mu.Unlock()
	ctxMu.Lock()
	ctx, ctxStop = context.WithCancel(context.Background())
	ctxMu.Unlock()
}

// afterStop holds the restart hook. A replacement process may only be spawned
// once the listener is closed, otherwise it races the old process for the
// port (Windows cannot rebind a port held by a live listener at all), so main
// runs the hook after fxApp.Stop rather than the requester spawning eagerly.
var (
	afterMu   sync.Mutex
	afterStop func()
)

// RestartAfterStop registers fn to run once this process has drained, then
// requests the stop. fn runs in the process that is on its way out.
func RestartAfterStop(fn func()) {
	afterMu.Lock()
	afterStop = fn
	afterMu.Unlock()
	RequestStop()
}

// RunAfterStop runs the registered restart hook, if any. main calls it once
// fxApp.Stop has returned and the listening port is free.
func RunAfterStop() {
	afterMu.Lock()
	fn := afterStop
	afterStop = nil
	afterMu.Unlock()
	if fn != nil {
		fn()
	}
}

// Cancel triggers shutdown, closing the Done channel. Safe to call multiple times.
func Cancel() {
	mu.Lock()
	defer mu.Unlock()
	if fired {
		return
	}
	fired = true
	close(done)
	ctxMu.Lock()
	defer ctxMu.Unlock()
	ctxStop()
}
