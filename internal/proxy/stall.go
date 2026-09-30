package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"9router/proxy/internal/log"
	"9router/proxy/internal/shutdown"
)

// DefaultStallTimeout is the default maximum idle time (no data) before an SSE
// stream is considered stalled and the connection is closed.
// Matches Next.js STREAM_STALL_TIMEOUT_MS = 360000 (6 minutes).
const DefaultStallTimeout = 6 * time.Minute

// ErrStreamStall reports that the stall watchdog fired, so a pending Read
// fails with this instead of the bare "file already closed" the Close would
// otherwise produce. Callers that must tell a timeout apart from a lost
// socket (an in-band SSE error frame, for instance) match on it.
var ErrStreamStall = errors.New("stream stall timeout")

// StallReader wraps an io.ReadCloser with a timer that fires if no data is
// read within the timeout. When the timer fires, the underlying reader is
// closed, which unblocks any pending Read call.
// Matches Next.js stall detection in pipeWithDisconnect.
type StallReader struct {
	reader   io.ReadCloser
	timer    *time.Timer
	timeout  time.Duration
	once     sync.Once
	doneOnce sync.Once
	done     chan struct{} // closed on Close; stops the shutdown watcher
	stalled   atomic.Bool // watchdog fired; Read reports ErrStreamStall
}

// NewStallReader wraps rc with stall detection. If no data is read within
// timeout, the reader is closed and subsequent reads return an error. It also
// closes the reader when process shutdown begins, so in-flight SSE streams end
// promptly instead of holding server.Shutdown until its deadline.
func NewStallReader(rc io.ReadCloser, timeout time.Duration, label string) io.ReadCloser {
	return NewStallReaderWithContext(context.Background(), rc, timeout, label)
}

// NewStallReaderWithContext wraps rc with stall detection and context cancellation.
// In addition to stall detection and process shutdown, if ctx is canceled (e.g. client
// disconnects / aborts), the reader is immediately closed to unblock pending reads
// and prevent socket leaks on Windows (FIN_WAIT_1 / CLOSE_WAIT).
func NewStallReaderWithContext(ctx context.Context, rc io.ReadCloser, timeout time.Duration, label string) io.ReadCloser {
	if timeout <= 0 {
		timeout = DefaultStallTimeout
	}
	s := &StallReader{
		reader:  rc,
		timeout: timeout,
		done:    make(chan struct{}),
	}
	s.timer = time.AfterFunc(timeout, func() {
		log.Warn("stream", "stall detected", "label", label, "timeout", timeout)
		s.stalled.Store(true)
		s.Close()
	})
	go func() {
		if ctx != nil && ctx.Done() != nil {
			select {
			case <-shutdown.Done():
				log.Info("stream", "shutdown, closing stream", "label", label)
				s.Close()
			case <-ctx.Done():
				log.Info("stream", "client context canceled, closing stream", "label", label)
				s.Close()
			case <-s.done:
			}
		} else {
			select {
			case <-shutdown.Done():
				log.Info("stream", "shutdown, closing stream", "label", label)
				s.Close()
			case <-s.done:
			}
		}
	}()
	return s
}

// Read implements io.Reader. Each call resets the stall timer.
func (s *StallReader) Read(p []byte) (int, error) {
	s.timer.Reset(s.timeout)
	n, err := s.reader.Read(p)
	if err != nil {
		s.timer.Stop()
	}
	// The watchdog closes the reader out from under this Read, so the raw
	// error is an opaque "file already closed". Translate it back into the
	// timeout it actually was, so callers can tell a stall from a lost socket.
	if err != nil && s.stalled.Load() {
		err = fmt.Errorf("read upstream after stall: %w", ErrStreamStall)
	}
	return n, err
}

// Close implements io.Closer. Stops the stall timer, stops the shutdown
// watcher, and closes the reader. Idempotent and synchronized with the timer
// and shutdown goroutines: whichever of Close, the stall-fire, or the shutdown
// watcher runs first closes the reader exactly once via s.once.
func (s *StallReader) Close() error {
	s.timer.Stop()
	s.doneOnce.Do(func() { close(s.done) })
	var err error
	s.once.Do(func() { err = s.reader.Close() })
	return err
}
