package proxy

import (
	"io"
	"log"
	"sync"
	"time"
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
	reader  io.ReadCloser
	timer   *time.Timer
	timeout time.Duration
	once    sync.Once
}

// NewStallReader wraps rc with stall detection. If no data is read within
// timeout, the reader is closed and subsequent reads return an error.
func NewStallReader(rc io.ReadCloser, timeout time.Duration, label string) io.ReadCloser {
	if timeout <= 0 {
		timeout = DefaultStallTimeout
	}
	s := &StallReader{
		reader:  rc,
		timeout: timeout,
	}
	s.timer = time.AfterFunc(timeout, func() {
		log.Printf("[stream] stall detected for %s: no data for %v", label, timeout)
		s.once.Do(func() { rc.Close() })
	})
	return s
}

// Read implements io.Reader. Each call resets the stall timer.
func (s *StallReader) Read(p []byte) (int, error) {
	s.timer.Reset(s.timeout)
	return s.reader.Read(p)
}

// Close implements io.Closer. Stops the stall timer and closes the reader.
func (s *StallReader) Close() error {
	s.timer.Stop()
	return s.reader.Close()
}
