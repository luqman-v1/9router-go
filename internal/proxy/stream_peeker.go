package proxy

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
	"sync"
)

// ErrStreamingZeroContent indicates that an upstream SSE stream completed without producing usable content.
var ErrStreamingZeroContent = errors.New("streaming upstream finished without content")

// ErrStreamingInbandError is returned by Finish when the stream carried a
// provider-injected error event (e.g. Anthropic `event: error` or OpenAI
// `data: {"error":...}`). The peeker commits the error frame to the client
// before returning this error, so the caller must stop the chain and must not
// fail over to the next combo member.
var ErrStreamingInbandError = errors.New("streaming upstream carried an in-band error")

// inbandFinishError wraps ErrStreamingInbandError while keeping the provider's
// message for logging. errors.Is against ErrStreamingInbandError reports true.
type inbandFinishError struct {
	msg string
}

func (e *inbandFinishError) Error() string {
	if e.msg != "" {
		return ErrStreamingInbandError.Error() + ": " + e.msg
	}
	return ErrStreamingInbandError.Error()
}

func (e *inbandFinishError) Unwrap() error { return ErrStreamingInbandError }

// bannerHoldLimit bounds how many bytes the peeker holds while waiting for a
// split frame to complete. A frame that never parses within this budget is
// committed rather than buffered: the peeker's job is to protect the turn, not
// to hold an unbounded buffer for an upstream that writes broken JSON.
const bannerHoldLimit = 64 * 1024

// HasSSEContent reports whether an SSE chunk carries client-visible assistant content
// (visible text, reasoning/thinking, or tool calls).
// Delta role, empty reasoning/thinking, and empty finish frames do not count as content.
func HasSSEContent(chunk []byte) bool {
	if len(chunk) == 0 {
		return false
	}
	// One write can contain multiple SSE events. Evaluate each line separately:
	// an empty content_block_start must not mask a later non-empty text delta.
	s := string(chunk)
	for {
		line, rest, more := strings.Cut(s, "\n")
		if hasSSEContentLine(line) {
			return true
		}
		if !more {
			return false
		}
		s = rest
	}
}

func hasSSEContentLine(s string) bool {
	// OpenAI chat completions
	if strings.Contains(s, `"content":"`) {
		// Ensure it's not empty content: ""
		if !strings.Contains(s, `"content":""`) && !strings.Contains(s, `"content":null`) {
			return true
		}
	}
	if strings.Contains(s, `"reasoning_content":"`) && !strings.Contains(s, `"reasoning_content":""`) && !strings.Contains(s, `"reasoning_content":null`) {
		return true
	}
	if strings.Contains(s, `"reasoning":"`) && !strings.Contains(s, `"reasoning":""`) && !strings.Contains(s, `"reasoning":null`) {
		return true
	}
	if strings.Contains(s, `"tool_calls":[`) && !strings.Contains(s, `"tool_calls":[]`) {
		return true
	}
	// Anthropic Messages
	if strings.Contains(s, `"type":"content_block_delta"`) && strings.Contains(s, `"text":"`) {
		if !strings.Contains(s, `"text":""`) {
			return true
		}
	}
	if strings.Contains(s, `"type":"content_block_delta"`) && strings.Contains(s, `"thinking":"`) {
		if !strings.Contains(s, `"thinking":""`) && !strings.Contains(s, `"thinking":null`) {
			return true
		}
	}
	if strings.Contains(s, `"type":"content_block_delta"`) && strings.Contains(s, `"signature_delta"`) {
		return true
	}
	if strings.Contains(s, `"type":"content_block_start"`) {
		if strings.Contains(s, `"type":"tool_use"`) ||
			strings.Contains(s, `"type":"thinking"`) ||
			strings.Contains(s, `"type":"redacted_thinking"`) {
			return true
		}
	}
	if strings.Contains(s, `"redacted_thinking"`) {
		return true
	}
	// Responses API
	if strings.Contains(s, `"response.output_text.delta"`) || strings.Contains(s, `"response.output_item.added"`) {
		return true
	}
	if strings.Contains(s, `"response.reasoning_text.delta"`) ||
		strings.Contains(s, `"response.reasoning_summary_text.delta"`) ||
		strings.Contains(s, `"response.reasoning_summary_text.done"`) {
		if !strings.Contains(s, `"delta":""`) && !strings.Contains(s, `"text":""`) {
			return true
		}
	}
	return false
}

// StreamContentPeeker buffers initial SSE frames until content is observed.
type StreamContentPeeker struct {
	http.ResponseWriter
	flusher        http.Flusher
	mu             sync.Mutex
	committed      bool
	sawContent     bool
	sawInbandError bool
	inbandMsg      string
	// bannerText accumulates the assistant text of frames held open because
	// they look like an upstream fault banner (see IsBannerSuspect).
	bannerText string
	// heldContent marks that the buffer holds an unparsed content frame rather
	// than preamble (role, finish_reason, [DONE]). Only the former is relayed
	// on Finish: preamble alone is a zero-content turn.
	heldContent bool
	buf         bytes.Buffer
	header      http.Header
	statusCode  int
}

func NewStreamContentPeeker(w http.ResponseWriter) *StreamContentPeeker {
	var f http.Flusher
	if fl, ok := w.(http.Flusher); ok {
		f = fl
	}
	return &StreamContentPeeker{
		ResponseWriter: w,
		flusher:        f,
		header:         make(http.Header),
		statusCode:     http.StatusOK,
	}
}

func (p *StreamContentPeeker) Header() http.Header {
	if p.committed {
		return p.ResponseWriter.Header()
	}
	return p.header
}

func (p *StreamContentPeeker) WriteHeader(statusCode int) {
	if p.committed {
		p.ResponseWriter.WriteHeader(statusCode)
		return
	}
	p.statusCode = statusCode
}

func (p *StreamContentPeeker) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.committed {
		return p.ResponseWriter.Write(b)
	}

	// An in-band error frame is an explicit failure signal; commit and emit it directly
	// so the caller/client receives the in-band error frame as intended (e.g. TestComboSkipsInbandErrorOnRetry).
	for _, line := range bytes.Split(b, []byte("\n")) {
		if _, msg, isErr := DetectInbandSSEError(line); isErr {
			p.sawContent = true
			p.sawInbandError = true
			p.inbandMsg = msg
			p.commitLocked()
			return p.ResponseWriter.Write(b)
		}
	}

	// A "content" frame is not proof of an answer: Antigravity reports an
	// upstream fault as ordinary assistant text on HTTP 200. The decision is
	// made on COMPLETE frames only, because the two questions it needs have
	// different answers mid-frame:
	//
	//   - does the frame parse? An unparseable write is the first half of a
	//     frame still arriving. Hold it, regardless of what heuristics see:
	//     a banner frame split BEFORE the `content` field matches nothing at
	//     all (HasSSEContent reads substrings of a JSON that is not there
	//     yet) and would otherwise be relayed unclassified. The held bytes
	//     are classified when the next write completes the frame.
	//   - does the frame carry an answer? A complete frame whose text is
	//     banner-shaped is held so the turn can be retried; one whose text is
	//     an answer commits; a complete frame with NO text is structural
	//     (Anthropic thinking / signature_delta) and commits, because
	//     holding those starves the first-output gate — but a frame with no
	//     text AND no structural markers (role-only, finish-only) is the
	//     zero-content turn the caller retries, so it is dropped.
	if p.heldContent {
		return p.writeHeld(b)
	}
	if !FrameParses(b) {
		return p.writeIncomplete(b)
	}

	// One or more complete frames. The banner scan is a byte scan over the
	// front of the frame, not a JSON decode: a healthy frame costs ~0.2 µs
	// instead of ~1.8 µs, and only a frame whose text actually looks like a
	// banner reaches the regex verdict.
	if text, suspect := chunkNeedsBannerCheck(b); suspect {
		// A complete frame whose whole text is still banner-shaped (or a
		// prefix of a lead-in): hold so the next frame can either complete
		// the banner or outgrow it.
		p.heldContent = true
		p.bannerText = text
		p.buf.Write(b)
		return len(b), nil
	}
	// A frame with no text and no structural marker is not content: a
	// role-only or finish-only frame is the zero-content turn the caller
	// retries, and committing it would answer 200 with nothing.
	if !HasSSEContent(b) {
		return len(b), nil
	}
	p.sawContent = true
	// Commit before relaying: the peeker's own header map and status code are
	// only applied there, and every later write short-circuits on `committed`
	// — one classification per turn, not per frame.
	p.commitLocked()
	return p.ResponseWriter.Write(b)
}

// writeIncomplete handles a write that holds no complete frame: the first
// half of a split frame (hold it — the next write completes it), or a
// keep-alive/[DONE]-only write (drop it — holding it would leave bytes that
// Finish would relay as if they were content).
func (p *StreamContentPeeker) writeIncomplete(b []byte) (int, error) {
	if !hasOpenFrame(b) {
		// No data frame is even started: keep-alives, comments, [DONE]. They
		// are never a banner and never content.
		return len(b), nil
	}
	if p.buf.Len()+len(b) >= bannerHoldLimit {
		// The budget is spent: relay rather than buffer an unbounded stream
		// for an upstream that never completes a frame.
		p.sawContent = true
		p.buf.Write(b)
		p.commitLocked()
		return len(b), nil
	}
	p.heldContent = true
	p.buf.Write(b)
	return len(b), nil
}

// writeHeld appends b to the held buffer and classifies the join. It is the
// only path a split frame takes, whichever half the split lands in.
func (p *StreamContentPeeker) writeHeld(b []byte) (int, error) {
	chunk := append(p.buf.Bytes(), b...)
	p.buf.Reset()

	// Still no complete frame: keep holding, bounded.
	if !FrameParses(chunk) {
		if !hasOpenFrame(chunk) {
			// The held bytes were keep-alives after all; drop them.
			p.heldContent = false
			return len(b), nil
		}
		if len(chunk) < bannerHoldLimit {
			p.heldContent = true
			p.buf.Write(chunk)
			return len(b), nil
		}
		// The budget is spent: relay rather than buffer an unbounded stream.
		p.heldContent = false
		p.sawContent = true
		p.buf.Write(chunk)
		p.commitLocked()
		return len(b), nil
	}

	text := ExtractSSEText(chunk)
	switch {
	case text != "" && IsBannerSuspect(text):
		// The complete text is still banner-shaped (or a prefix of a lead-in):
		// hold the completed frames and wait for the next one.
		p.heldContent = true
		p.bannerText = text
		p.buf.Write(chunk)
		return len(b), nil
	case text == "" && !HasSSEContent(chunk):
		// Zero-content turn: the held bytes carried no prose and no
		// structural marker. Drop them; Finish fails the turn.
		p.heldContent = false
		return len(b), nil
	default:
		// An answer or a structural frame: relay everything held byte for
		// byte, then commit.
		p.heldContent = false
		p.sawContent = true
		p.bannerText = text
		p.buf.Write(chunk)
		p.commitLocked()
		return len(b), nil
	}
}

// hasOpenFrame reports whether b holds a `data:` line that started a JSON
// object which has not finished — the first half of a split frame. A write
// whose data lines are all [DONE], or that has none, has nothing to hold.
func hasOpenFrame(b []byte) bool {
	for _, line := range bytes.Split(b, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if !bytes.HasPrefix(trimmed, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(trimmed[len("data:"):])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		if len(payload) > 0 && payload[0] == '{' && !frameComplete(payload) {
			return true
		}
	}
	return false
}

func (p *StreamContentPeeker) commitLocked() {
	if p.committed {
		return
	}
	p.committed = true
	targetH := p.ResponseWriter.Header()
	for k, vv := range p.header {
		for _, v := range vv {
			targetH.Add(k, v)
		}
	}
	p.ResponseWriter.WriteHeader(p.statusCode)
	if p.buf.Len() > 0 {
		_, _ = p.ResponseWriter.Write(p.buf.Bytes())
		p.buf.Reset()
	}
	if p.flusher != nil {
		p.flusher.Flush()
	}
}

func (p *StreamContentPeeker) Finish() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.sawInbandError {
		return &inbandFinishError{msg: p.inbandMsg}
	}
	if !p.sawContent {
		// A frame held because its JSON never completed: the upstream stopped
		// mid-frame, so there is nothing to classify. The held bytes ARE the
		// stream's content — an upstream that writes broken JSON still wrote
		// something the client asked to see — so relay them rather than failing
		// the turn. The zero-content verdict is for a stream that carried
		// nothing at all (role + finish_reason only), which holds no bytes.
		// Held bytes with no text that matched a banner are an unparsed frame:
		// relay them. Held bytes whose text IS banner-shaped are a fault
		// banner, and relaying it would show the client an error message as if
		// it were the answer — that is the zero-content failure below.
		if p.heldContent && p.buf.Len() > 0 && !IsBannerSuspect(p.bannerText) {
			p.sawContent = true
			p.commitLocked()
			return nil
		}
		// A turn whose only content was a fault banner is a failure, not an
		// answer: the caller retries it the same way it retries an empty turn.
		return ErrStreamingZeroContent
	}
	if !p.committed {
		p.commitLocked()
	}
	return nil
}
