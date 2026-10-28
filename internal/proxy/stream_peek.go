package proxy

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"strings"

	"9router/proxy/internal/handlerutil"
)

const (
	// DefaultPeekBytes bounds how much of a stream is read before the caller
	// is allowed to commit headers. One complete event is the normal stopping
	// point; this is the ceiling for an upstream whose events are large or
	// whose frames are all preamble.
	DefaultPeekBytes = 16 * 1024

	// MaxPeekBytes is the hard ceiling. It exists so a caller cannot ask for an
	// unbounded buffer: peeking happens before anything is written, so whatever
	// is read has to be held in memory and replayed.
	MaxPeekBytes = 10 * 1024 * 1024
)

// PeekFirstStreamEvent reads just enough of an event stream to decide whether
// the upstream is actually serving a turn, and returns the bytes it consumed
// along with a reader that reproduces them exactly.
//
// The problem it solves: an upstream can answer HTTP 200 and then send nothing
// — an immediate EOF, keep-alive comments, or a bare `[DONE]`. Once the gateway
// has written `200 text/event-stream` for such a response it can no longer fall
// back, so a combo settles on the model that said nothing. Peeking first keeps
// the decision reversible: refuse before the commit and the fallback loop runs
// normally.
//
// The returned reader is io.MultiReader(peeked, r), so the caller relays the
// original bytes — CRLF, comments and blank lines included — rather than a
// re-serialised stream.
//
// maxBytes <= 0 selects DefaultPeekBytes; anything above MaxPeekBytes is
// clamped. The peek stops at the first event that carries an answer (or at the
// budget, whichever comes first), which is why a healthy stream costs one
// round-trip's worth of buffering rather than the whole turn.
func PeekFirstStreamEvent(ctx context.Context, r io.Reader, maxBytes int) ([]byte, io.Reader, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if maxBytes <= 0 {
		maxBytes = DefaultPeekBytes
	}
	if maxBytes > MaxPeekBytes {
		maxBytes = MaxPeekBytes
	}

	buf := &peekBuffer{limit: maxBytes}
	lr := &peekReader{r: r, ctx: ctx, buf: buf}
	err := ScanStreamEvents(lr, func(ev SSEEvent) bool {
		// A failure can arrive as a labelled frame (`event: error`) or as a
		// plain data frame whose payload carries an `error` object — OpenRouter
		// relays a dead downstream the second way on HTTP 200. Both are
		// reversible only until the headers are written.
		if ev.Event == "error" || inbandErrorCarried(ev.Data) {
			buf.refuse(inbandEventError(ev))
			return false
		}
		return !buf.accept(ev)
	})

	peeked, replay := buf.finish(r)
	switch {
	case buf.refusal != nil:
		return nil, nil, buf.refusal
	case err != nil && !errors.Is(err, io.EOF):
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, nil, ctxErr
		}
		if lr.err != nil {
			return nil, nil, lr.err
		}
		return nil, nil, err
	case !buf.carried && !buf.overflow:
		return nil, nil, UpstreamFailure(http.StatusBadGateway, NoCompletionInStream)
	}
	return peeked, replay, nil
}

// peekBuffer accumulates the raw bytes the scanner consumed and records
// whether anything a client can read has arrived yet.
type peekBuffer struct {
	limit    int
	raw      bytes.Buffer
	carried  bool
	overflow bool
	refusal  error
}

// accept records one event. It reports whether the peek is finished: true as
// soon as the turn is known to be healthy, which is what makes the peek cost
// one event instead of one response.
func (b *peekBuffer) accept(ev SSEEvent) bool {
	if b.carried {
		return true
	}
	if streamEventCarriesAnswer(ev.Data) {
		b.carried = true
		return true
	}
	return b.overflow
}

// refuse records the failure that ends the peek early.
func (b *peekBuffer) refuse(err error) {
	if b.refusal == nil {
		b.refusal = err
	}
}

// finish returns the raw peeked bytes and a reader that replays them ahead of
// the untouched upstream.
func (b *peekBuffer) finish(r io.Reader) ([]byte, io.Reader) {
	peeked := append([]byte(nil), b.raw.Bytes()...)
	return peeked, io.MultiReader(bytes.NewReader(peeked), r)
}

// peekReader tees every byte the scanner reads into the peek buffer, stopping
// the scan once the budget is spent.
//
// bufio.Scanner cannot be told "stop before the next line", and the scanner is
// the only thing that understands event framing, so the bound is enforced
// where the bytes pass through. A short read at the limit is deliberate: the
// remaining bytes are still in r, which is what the replay reader picks up.
type peekReader struct {
	r    io.Reader
	ctx  context.Context
	buf  *peekBuffer
	err  error
	done bool
}

func (p *peekReader) Read(b []byte) (int, error) {
	if p.done {
		return 0, io.EOF
	}
	if err := p.ctx.Err(); err != nil {
		return 0, err
	}
	room := p.buf.limit - p.buf.raw.Len()
	if room <= 0 {
		p.buf.overflow = true
		p.done = true
		return 0, io.EOF
	}
	if len(b) > room {
		b = b[:room]
	}
	n, err := p.r.Read(b)
	if n > 0 {
		p.buf.raw.Write(b[:n])
	}
	if err != nil && err != io.EOF {
		p.err = err
	}
	if room-n <= 0 {
		p.buf.overflow = true
		p.done = true
	}
	return n, err
}

// streamEventCarriesAnswer reports whether one event's payload holds something
// a client can act on. It reuses the non-stream probe so the two paths cannot
// disagree about what "an answer" means.
//
// It only refuses a payload it recognises as an empty chat or Responses
// envelope. Anything else commits: the peek sits in front of every streamed
// turn, so a shape the probe does not model — Anthropic-native SSE, a vendor's
// own event vocabulary — must not be mistaken for silence. Refusing those
// would trade a missing fallback for a broken provider.
func streamEventCarriesAnswer(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("[DONE]")) {
		return false
	}
	if trimmed[0] != '{' {
		return true // opaque payload: relay rather than guess
	}
	var probe completionProbe
	if err := json.Unmarshal(trimmed, &probe, handlerutil.UpstreamBody); err != nil {
		return true
	}
	if probe.Choices == nil && probe.Content == nil && probe.Output == nil {
		return true // an envelope this probe does not model
	}
	return missingCompletion(probe) == ""
}

// inbandErrorCarried reports whether an event's data holds a provider error
// envelope.
func inbandErrorCarried(data []byte) bool {
	_, _, found := DetectInbandSSEError(append([]byte("data: "), data...))
	return found
}

// inbandEventError turns a provider error frame into the failure the fallback
// layer retries on, preserving the provider's own status when it gave one.
func inbandEventError(ev SSEEvent) error {
	if code, msg, found := DetectInbandSSEError(append([]byte("data: "), ev.Data...)); found {
		return UpstreamFailure(code, msg)
	}
	if msg := strings.TrimSpace(string(ev.Data)); msg != "" {
		return UpstreamFailure(http.StatusBadGateway, InbandStreamErrorMessage+": "+msg)
	}
	return UpstreamFailure(http.StatusBadGateway, InbandStreamErrorMessage)
}
