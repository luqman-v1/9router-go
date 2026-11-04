package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
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

// DefaultFirstEventTimeout is the default deadline for the first SSE event
// from Cloud Code Assist (OMP DEFAULT_FIRST_EVENT_TIMEOUT_MS=300000, shared
// watchdog; Flash overrides in KDL).
const DefaultFirstEventTimeout = 5 * time.Minute

// FlashFirstEventTimeout is the tighter first-event budget for Gemini Flash
// families (OMP classes/gemini.kdl stream-first-event-timeout-ms 60000).
const FlashFirstEventTimeout = 60 * time.Second

// ResolveDefaultStallTimeout reads environment variables STREAM_STALL_TIMEOUT_MS
// and NINEROUTER_STREAM_STALL_TIMEOUT_MS. If set to a positive integer (milliseconds),
// returns that duration. Otherwise returns DefaultStallTimeout.
func ResolveDefaultStallTimeout() time.Duration {
	for _, envVar := range []string{"STREAM_STALL_TIMEOUT_MS", "NINEROUTER_STREAM_STALL_TIMEOUT_MS"} {
		if val := os.Getenv(envVar); val != "" {
			if ms, err := strconv.Atoi(val); err == nil && ms > 0 {
				return time.Duration(ms) * time.Millisecond
			}
		}
	}
	return DefaultStallTimeout
}

// ErrStreamStall reports that the stall watchdog fired, so a pending Read
// fails with this instead of the bare "file already closed" the Close would
// otherwise produce. Callers that must tell a timeout apart from a lost
// socket (an in-band SSE error frame, for instance) match on it.
var ErrStreamStall = errors.New("stream stall timeout")

// ErrStreamFirstEvent reports that no first event arrived within the
// provider-owned first-event deadline. Antigravity/Cloud Code Assist can
// return headers then never emit an SSE event; the distinction lets
// callers fail over to a fallback endpoint before writing to the client.
var ErrStreamFirstEvent = errors.New("stream first event timeout")

// StallReader wraps an io.ReadCloser with a timer that fires if no data is
// read within the timeout. When the timer fires, the underlying reader is
// closed, which unblocks any pending Read call.
// Matches Next.js stall detection in pipeWithDisconnect.
type StallReader struct {
	reader            io.ReadCloser
	timer             *time.Timer
	timeout           time.Duration
	firstEventTimeout time.Duration
	firstSeen         atomic.Bool
	firstExpired      atomic.Bool
	once              sync.Once
	doneOnce          sync.Once
	done              chan struct{} // closed on Close; stops the shutdown watcher
	stalled           atomic.Bool   // watchdog fired; Read reports ErrStreamStall
}

// NewStallReaderWithFirstEvent wraps rc with a dual watchdog: the first
// event must arrive within firstEventTimeout (else ErrStreamFirstEvent),
// then idle gaps are bounded by timeout. A zero firstEventTimeout disables
// the first-event arm and behaves like NewStallReaderWithContext.
func NewStallReaderWithFirstEvent(ctx context.Context, rc io.ReadCloser, timeout, firstEventTimeout time.Duration, label string) io.ReadCloser {
	if timeout <= 0 {
		timeout = ResolveDefaultStallTimeout()
	}
	if firstEventTimeout <= 0 {
		return NewStallReaderWithContext(ctx, rc, timeout, label)
	}
	s := &StallReader{reader: rc, timeout: timeout, firstEventTimeout: firstEventTimeout, done: make(chan struct{})}
	s.timer = time.AfterFunc(firstEventTimeout, func() {
		if s.firstSeen.Load() {
			return
		}
		s.firstExpired.Store(true)
		log.Warn("stream", "first event timeout", "label", label, "timeout", firstEventTimeout)
		s.stalled.Store(true)
		_ = s.Close()
	})
	go func() {
		if ctx != nil && ctx.Done() != nil {
			select {
			case <-shutdown.Done():
				log.Info("stream", "shutdown, closing stream", "label", label)
				_ = s.Close()
			case <-ctx.Done():
				log.Info("stream", "client context canceled, closing stream", "label", label)
				_ = s.Close()
			case <-s.done:
			}
		} else {
			select {
			case <-shutdown.Done():
				log.Info("stream", "shutdown, closing stream", "label", label)
				_ = s.Close()
			case <-s.done:
			}
		}
	}()

	return s
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
		timeout = ResolveDefaultStallTimeout()
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

// Read implements io.Reader. Each call resets the stall timer. For a dual
// watchdog the first non-empty read flips the budget from firstEventTimeout
// to the steady-state idle timeout; an expiry before any data reports
// ErrStreamFirstEvent so callers can fail over before writing.
func (s *StallReader) Read(p []byte) (int, error) {
	if !s.firstSeen.Load() && s.firstEventTimeout > 0 {
		s.timer.Reset(s.firstEventTimeout)
	} else {
		s.timer.Reset(s.timeout)
	}
	n, err := s.reader.Read(p)
	if n > 0 && !s.firstSeen.Load() {
		s.firstSeen.Store(true)
		if s.firstEventTimeout > 0 {
			s.timer.Reset(s.timeout)
		}
	}
	if err != nil {
		s.timer.Stop()
	}
	// The watchdog closes the reader out from under this Read, so the raw
	// error is an opaque "file already closed". Translate it back into the
	// timeout it actually was, so callers can tell a stall from a lost socket.
	if err != nil && s.stalled.Load() {
		if s.firstExpired.Load() {
			err = fmt.Errorf("read upstream before first event: %w", ErrStreamFirstEvent)
		} else {
			err = fmt.Errorf("read upstream after stall: %w", ErrStreamStall)
		}
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
