package proxy

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"
)

// WriteSSEHeaders sets standard SSE headers on the response and writes HTTP 200.
// It sets anti-buffering headers (X-Accel-Buffering: no, Cache-Control: no-cache, no-transform)
// so reverse proxies, local sidecars, and HTTP clients do not hold back or coalesce chunks.
// Returns the http.Flusher if the ResponseWriter supports it.
func WriteSSEHeaders(w http.ResponseWriter) http.Flusher {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	f, _ := w.(http.Flusher)
	if f != nil {
		f.Flush()
	}
	return f
}

// SSECopy lives in sse_copy.go.

// DefaultHeartbeatInterval is the default period for sending SSE keep-alive ping comments.
// Set to 15 seconds so strict clients (Oh My Pi / Cline / Roo) with 30-60s idle timeouts
// never consider the stream stalled during prolonged thinking/reasoning phases.
const DefaultHeartbeatInterval = 15 * time.Second

// HeartbeatWriter wraps an http.ResponseWriter to periodically emit SSE keep-alive
// comments (": keep-alive\n\n") when no data has been written for the interval.
// Thread-safe: synchronizes concurrent writes, flushes, and heartbeat ticks.
type HeartbeatWriter struct {
	w         http.ResponseWriter
	flusher   http.Flusher
	interval  time.Duration
	lastWrite time.Time
	mu        sync.Mutex
	stopCh    chan struct{}
	done      bool
}

// NewHeartbeatWriter starts a background ticker that emits ": keep-alive\n\n"
// every interval if no writes occurred since the last interval.
// Call Close() when the stream completes to terminate the ticker.
func NewHeartbeatWriter(ctx context.Context, w http.ResponseWriter, interval time.Duration) *HeartbeatWriter {
	if interval <= 0 {
		interval = DefaultHeartbeatInterval
	}
	flusher, _ := w.(http.Flusher)
	hw := &HeartbeatWriter{
		w:         w,
		flusher:   flusher,
		interval:  interval,
		lastWrite: time.Now(),
		stopCh:    make(chan struct{}),
	}
	go func() {
		ticker := time.NewTicker(hw.interval)
		defer ticker.Stop()
		for {
			if ctx != nil && ctx.Done() != nil {
				select {
				case <-hw.stopCh:
					return
				case <-ctx.Done():
					_ = hw.Close()
					return
				case <-ticker.C:
					hw.mu.Lock()
					if hw.done {
						hw.mu.Unlock()
						return
					}
					if time.Since(hw.lastWrite) >= hw.interval {
						if _, err := hw.w.Write([]byte(": keep-alive\n\n")); err == nil {
							if hw.flusher != nil {
								hw.flusher.Flush()
							}
						}
					}
					hw.mu.Unlock()
				}
			} else {
				select {
				case <-hw.stopCh:
					return
				case <-ticker.C:
					hw.mu.Lock()
					if hw.done {
						hw.mu.Unlock()
						return
					}
					if time.Since(hw.lastWrite) >= hw.interval {
						if _, err := hw.w.Write([]byte(": keep-alive\n\n")); err == nil {
							if hw.flusher != nil {
								hw.flusher.Flush()
							}
						}
					}
					hw.mu.Unlock()
				}
			}
		}
	}()
	return hw
}

func (hw *HeartbeatWriter) Header() http.Header {
	return hw.w.Header()
}

func (hw *HeartbeatWriter) WriteHeader(statusCode int) {
	hw.mu.Lock()
	defer hw.mu.Unlock()
	hw.w.WriteHeader(statusCode)
}

func (hw *HeartbeatWriter) Write(b []byte) (int, error) {
	hw.mu.Lock()
	defer hw.mu.Unlock()
	if hw.done {
		return 0, io.ErrClosedPipe
	}
	hw.lastWrite = time.Now()
	n, err := hw.w.Write(b)
	return n, err
}

func (hw *HeartbeatWriter) Flush() {
	hw.mu.Lock()
	defer hw.mu.Unlock()
	if hw.flusher != nil && !hw.done {
		hw.flusher.Flush()
	}
}

func (hw *HeartbeatWriter) Close() error {
	hw.mu.Lock()
	defer hw.mu.Unlock()
	if !hw.done {
		hw.done = true
		close(hw.stopCh)
	}
	return nil
}
