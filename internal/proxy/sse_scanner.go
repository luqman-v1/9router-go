package proxy

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"sync/atomic"
	"time"
)

// PendingCompletionFlushTimeout bounds the wait for a deferred terminal event.
//
// The Responses translator holds response.completed back when finish_reason
// arrives before the usage trailer (upstream PR #4476), so the client is owed
// that event. A broken chat upstream can then stall — no usage trailer, no
// [DONE], connection held open — and the deferral has to give up eventually or
// the client waits forever. Mirrors upstream's PENDING_COMPLETION_FLUSH_MS
// (decolua/9router fbcaa282 + 7111db35).
//
// A package var, not a const, so a test can drive the bound without sleeping
// through it.
var PendingCompletionFlushTimeout = 3 * time.Second

// SSEEvent is one complete event from an event stream, with the labels the
// chunk-level API used to drop.
//
// The labels are not decoration: a provider that reports a failure as
// `event: error` puts the failure in the label, and a payload-only reader sees
// a JSON object it has no reason to reject. ID is carried for the same reason
// on the other side — a caller replaying a stream keeps the ids it was given.
type SSEEvent struct {
	Event string // value of the `event:` field, empty when the stream omits it
	ID    string // value of the `id:` field, empty when absent
	Data  []byte // `data:` fields of the event, joined with \n
}

// ScanStream reads an SSE stream from r, accumulates complete events
// (per the SSE spec: fields separated by \n, an event ends at a blank
// line), joins all `data:` fields of each event into one payload, and
// invokes onChunk once per complete event.
//
// It correctly handles multi-line `data:` payloads, CRLF line endings, and
// `data:` keep-alives (empty payload → no callback). A single optional
// leading space after the colon is stripped per the spec.
func ScanStream(r io.Reader, onChunk func([]byte)) error {
	return ScanStreamEvents(r, func(ev SSEEvent) bool {
		if len(ev.Data) > 0 {
			onChunk(ev.Data)
		}
		return true
	})
}

// ScanStreamEvents is ScanStream with the event's own labels attached and with
// the option to stop reading early.
//
// Returning false from onEvent ends the scan without reading further and
// without reporting an error: the caller has what it came for, and draining a
// stream it will not use would cost the turn its latency budget. That is what
// lets a peek read one event instead of buffering the response.
func ScanStreamEvents(r io.Reader, onEvent func(ev SSEEvent) bool) error {
	return scanEvents(r, nil, onEvent)
}

// ScanStreamWithDeadline is ScanStream with an upper bound on the gap between
// two events: it returns once no event arrives within timeout of the last one.
// An upstream that streams a terminal chunk and then holds the connection open
// would otherwise pin the caller for as long as it likes, which is how a
// translated stream loses the response.completed its client is waiting for.
//
// The bound only applies once pending reports true. Before anything is owed to
// the client there is nothing to give up on, and a provider that pauses before
// its first token is slow, not broken. Each event gets its own window, so a
// stream that keeps flowing is never cut off however long the turn runs.
func ScanStreamWithDeadline(ctx context.Context, r io.Reader, timeout time.Duration, pending func() bool, onChunk func([]byte)) error {
	return scanEvents(r, newIdleWatch(ctx, timeout, pending), func(ev SSEEvent) bool {
		if len(ev.Data) > 0 {
			onChunk(ev.Data)
		}
		return true
	})
}

// idleWatch bounds how long the scanner may wait for the next line of a stream.
// bufio.Scanner blocks inside Read and takes no deadline, so the wait is
// covered by closing the reader: everything an SSE stream is built from (http
// body, io.Pipe, os.File) implements io.Closer, and a close unblocks the read
// already in flight.
type idleWatch struct {
	ctx     context.Context
	idle    time.Duration
	pending func() bool
	expired atomic.Bool
}

// newIdleWatch returns a watcher, or nil when timeout <= 0 disables the bound.
func newIdleWatch(ctx context.Context, idle time.Duration, pending func() bool) *idleWatch {
	if idle <= 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return &idleWatch{ctx: ctx, idle: idle, pending: pending}
}

// scan returns the next line, or false when the stream is over or the bound
// elapsed.
func (w *idleWatch) scan(scanner *bufio.Scanner, r io.Reader) bool {
	if w.pending != nil && !w.pending() {
		return scanner.Scan()
	}
	if w.ctx.Err() != nil {
		w.expired.Store(true)
		return false
	}
	ctx, cancel := context.WithTimeout(w.ctx, w.idle)
	released := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
		case <-released:
			return
		}
		w.expired.Store(true)
		if c, ok := r.(io.Closer); ok {
			_ = c.Close()
		}
	}()
	scanned := scanner.Scan()
	close(released)
	cancel()
	return scanned
}

// scanEvents is the shared ScanStream body.
func scanEvents(r io.Reader, w *idleWatch, onEvent func(SSEEvent) bool) error {
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 10*1024*1024) // Allow lines up to 10MB (tool calls, multimodal data)

	// With a bound in place the first line is read before the watch arms,
	// otherwise a stream that has already delivered its first event would wait
	// out a window it has earned. Every later read goes through the watch, and
	// bufio's own buffer is what makes the timer — not the arrival of the next
	// line — decide when the stream has gone quiet.
	var first []byte
	haveFirst := false
	if w != nil && scanner.Scan() {
		first = append(first[:0], scanner.Bytes()...)
		haveFirst = true
	}

	var dataLines [][]byte
	var event, id string
	emit := func() bool {
		defer func() { event, id = "", "" }()
		if len(dataLines) == 0 {
			return true
		}
		payload := bytes.Join(dataLines, []byte("\n"))
		dataLines = dataLines[:0]
		if len(payload) == 0 {
			return true // keep-alive: `data:` with empty payload
		}
		return onEvent(SSEEvent{Event: event, ID: id, Data: payload})
	}

	for {
		var line []byte
		switch {
		case haveFirst:
			line, haveFirst = first, false
		case w != nil && w.scan(scanner, r):
			line = scanner.Bytes()
		case w == nil && scanner.Scan():
			line = scanner.Bytes()
		default:
			emit()
			return scanErr(scanner, w)
		}

		// Blank line terminates the current event.
		if len(line) == 0 {
			if !emit() {
				return nil
			}
			continue
		}
		// Comment lines (": keep-alive") are ignored.
		if line[0] == ':' {
			continue
		}
		switch {
		case bytes.HasPrefix(line, []byte("data:")):
			chunk := bytes.TrimPrefix(line, []byte("data:"))
			// Per SSE spec, strip a single optional leading space.
			if len(chunk) > 0 && chunk[0] == ' ' {
				chunk = chunk[1:]
			}
			dataLines = append(dataLines, chunk)
		case bytes.HasPrefix(line, []byte("event:")):
			event = sseFieldValue(line, "event:")
		case bytes.HasPrefix(line, []byte("id:")):
			id = sseFieldValue(line, "id:")
		}
	}
}

// sseFieldValue reads the value of a named SSE field line, stripping the
// single optional leading space the spec allows after the colon.
func sseFieldValue(line []byte, prefix string) string {
	value := bytes.TrimPrefix(line, []byte(prefix))
	if len(value) > 0 && value[0] == ' ' {
		value = value[1:]
	}
	return string(value)
}

// scanErr reports the scanner's own error unless the watch ended the read: a
// break caused by the deadline is the expected outcome, not an upstream
// failure.
func scanErr(scanner *bufio.Scanner, w *idleWatch) error {
	err := scanner.Err()
	if err != nil && w != nil && w.expired.Load() {
		return nil
	}
	return err
}