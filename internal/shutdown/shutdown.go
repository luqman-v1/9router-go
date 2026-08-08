package shutdown

import "sync"

var (
	mu    sync.Mutex
	fired bool
	done  = make(chan struct{})
)

// Done returns a channel that is closed when shutdown begins.
// In-flight work (SSE streams, upstream readers) selects on it to stop promptly
// instead of holding server.Shutdown until its deadline.
func Done() <-chan struct{} { return done }

// Fired reports whether shutdown has been triggered.
func Fired() bool {
	mu.Lock()
	defer mu.Unlock()
	return fired
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
}
