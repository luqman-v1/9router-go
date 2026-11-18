package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/proxy"
)

// The OpenCode free-tier gate is unusual in two ways that only matter before
// the response head is committed:
//
//   - it sometimes answers a refusal inside an HTTP 200 SSE frame instead of a
//     4xx status, so a status-code check sees success;
//   - it can stall, or drop at EOF, before emitting any frame at all.
//
// Both arrive after 200 OK has been written by default, and once those bytes are
// on the wire the router's failover machinery can no longer act: the client is
// left holding either a hung stream or a "successful" turn containing an error
// frame. This file closes that window by reading exactly one SSE frame first.

const (
	// openCodeFirstEventTimeout bounds how long the gate waits for the first
	// complete data frame. Well inside a caller's patience but long enough to
	// clear a slow model's think-before-first-token delay.
	openCodeFirstEventTimeout = 20 * time.Second

	// openCodeFirstEventLabel names this reader in stall/shutdown logs.
	openCodeFirstEventLabel = "opencode-first-event"
)

// errOpenCodePreludeTooLarge reports that the upstream sent more bytes before
// its first data frame than the gate is willing to hold. An oversized prelude is
// a refusal in its own right: it means the stream is not the SSE the lane
// expects, and holding it would only delay the failover.
var errOpenCodePreludeTooLarge = fmt.Errorf("opencode: first-event prelude exceeded %d bytes", constants.UpstreamErrLimit)

// awaitOpenCodeFirstEvent reads up to the first complete SSE data frame and
// returns a reader that replays those bytes ahead of the live stream.
//
// The returned error is either an *proxy.UpstreamError carrying the status the
// refusal implies (so forwardOpenCodeRotating and the handler's pool walk can
// act on it exactly as they would on a real 4xx), or a transport error. A nil
// error means the stream is healthy and the caller must use the returned
// reader instead of the original body.
//
// The caller keeps ownership: on error the body is closed here, on success the
// returned reader closes it.
func awaitOpenCodeFirstEvent(ctx context.Context, resp *http.Response, timeout time.Duration) (io.ReadCloser, error) {
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("opencode: first event on a nil body")
	}
	// Only an event stream has frames to wait for. A provider that answers a
	// streamed request with one JSON document — every mock upstream in the test
	// suite does, and a gateway in front of the real one may too — is handled by
	// the ordinary body reader, and gating it here would reject a valid 200.
	if !isEventStreamResponse(resp.Header) {
		return resp.Body, nil
	}
	body := resp.Body
	if timeout <= 0 {
		timeout = openCodeFirstEventTimeout
	}
	// The stall reader owns the first-event deadline and the client-context
	// watcher, so a stalled upstream is closed rather than merely abandoned —
	// which is what unblocks the pending Read below.
	guarded := proxy.NewStallReaderWithFirstEvent(ctx, body, proxy.ResolveDefaultStallTimeout(), timeout, openCodeFirstEventLabel)

	head, err := readFirstOpenCodeFrame(guarded)
	if err != nil {
		_ = guarded.Close()
		return nil, err
	}
	if ue := openCodeInBandRefusal(head); ue != nil {
		_ = guarded.Close()
		return nil, ue
	}
	return replayBody{Reader: io.MultiReader(bytes.NewReader(head), guarded), Closer: guarded}, nil
}

// readFirstOpenCodeFrame accumulates bytes until one data frame is complete, the
// prelude cap is hit, or the reader fails.
func readFirstOpenCodeFrame(r io.Reader) ([]byte, error) {
	head := make([]byte, 0, 4096)
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			head = append(head, buf[:n]...)
			if openCodeFrameComplete(head) {
				return head, nil
			}
			if len(head) >= constants.UpstreamErrLimit {
				return nil, errOpenCodePreludeTooLarge
			}
		}
		if err != nil {
			// A stream that ends before its first complete frame is a failure,
			// not a short success: the caller has nothing to show the client.
			if len(head) > 0 && openCodeFrameComplete(head) {
				return head, nil
			}
			return nil, fmt.Errorf("opencode: first event: %w", err)
		}
	}
}

// openCodeFrameComplete reports whether head holds a terminated data frame: a
// `data:` line followed by the blank line that ends the frame.
func openCodeFrameComplete(head []byte) bool {
	idx := bytes.Index(head, []byte("data:"))
	if idx < 0 {
		return false
	}
	return bytes.Contains(head[idx:], []byte("\n\n"))
}

// openCodeInBandRefusal lifts a gate refusal out of the prelude. The status is
// the one the token implies, not the one the HTTP response carried — the whole
// point is that the response said 200.
func openCodeInBandRefusal(head []byte) *proxy.UpstreamError {
	switch {
	case bytes.Contains(head, []byte(openCodeUsageLimitToken)):
		return &proxy.UpstreamError{StatusCode: http.StatusTooManyRequests, Body: head}
	case bytes.Contains(head, []byte(openCodeRefusalToken)):
		return &proxy.UpstreamError{StatusCode: http.StatusForbidden, Body: head}
	}
	return nil
}
