package proxy

import (
	"io"
	"sync"
	"time"

	"9router/proxy/internal/log"
	"9router/proxy/internal/shutdown"
)

// DefaultStallTimeout is the default maximum idle time (no data) before an SSE
// stream is considered stalled and the connection is closed.
// Matches Next.js STREAM_STALL_TIMEOUT_MS = 360000 (6 minutes).
const DefaultStallTimeout = 6 * time.Minute

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
}

// NewStallReader wraps rc with stall detection. If no data is read within
// timeout, the reader is closed and subsequent reads return an error. It also
// closes the reader when process shutdown begins, so in-flight SSE streams end
// promptly instead of holding server.Shutdown until its deadline.
func NewStallReader(rc io.ReadCloser, timeout time.Duration, label string) io.ReadCloser {
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
		s.Close()
	})
	go func() {
		select {
		case <-shutdown.Done():
			log.Info("stream", "shutdown, closing stream", "label", label)
			s.Close()
		case <-s.done:
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
