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
	// upstream fault as ordinary assistant text on HTTP 200. Hold the stream
	// open while the text so far is banner-shaped (or still a prefix of a
	// banner lead-in) so the turn can be retried instead of showing the client
	// an error message as if it were the reply. Real text outgrows the banner
	// shape on the next frame and commits normally, and Finish fails open if
	// the stream never resolves either way.
	//
	// A frame SPLIT across writes must be held too: HasSSEContent matches a
	// substring, so the first half of a banner frame ("...\"content\":\"Agent exec")
	// reads as content while ExtractSSEText yields nothing, because the JSON is
	// not parseable yet. Committing there would relay the banner — the whole
	// failure this peeker exists to stop. The bytes are therefore classified
	// only once the frame parses, which the next write completes; the tail of
	// a split frame matches no per-line content heuristic at all, so a held
	// buffer makes this write a continuation and the joined bytes are what is
	// read.
	//
	// The common case is one write per complete frame: parse `b` alone, and
	// the only cost over the pre-banner behaviour is one ExtractSSEText per
	// content write. The join path below runs only when a frame is actually
	// split, which is the exception the first branch exists to catch.
	if HasSSEContent(b) && !p.heldContent {
		text := ExtractSSEText(b)
		if text != "" && !IsBannerSuspect(text) {
			p.sawContent = true
			p.bannerText = text
			// Commit before relaying: the peeker's own header map and status
			// code are only applied there, and every later write short-circuits
			// on `committed` — which is also what keeps the banner decision off
			// the rest of the stream (one classification per turn, not per
			// frame).
			p.commitLocked()
			return p.ResponseWriter.Write(b)
		}
		// Either a banner, or a frame whose JSON is incomplete (the content
		// substring matched but nothing parsed). Both are held: the first so
		// the turn can be retried, the second until the next write completes
		// the frame.
		p.heldContent = true
		p.bannerText = text
		p.buf.Write(b)
		return len(b), nil
	}
	// A continuation: the tail of a split frame matches no per-line content
	// heuristic at all, so the joined bytes are what is read. `text` is
	// assigned, never accumulated — the held bytes are re-parsed as part of
	// the join, and reading them twice would duplicate every held frame
	// ("Agent execution Agent execution ..."), which is no longer
	// banner-shaped and would commit the turn.
	if p.heldContent {
		chunk := append(append([]byte(nil), p.buf.Bytes()...), b...)
		text := ExtractSSEText(chunk)
		if text == "" && len(chunk) < bannerHoldLimit {
			p.buf.Write(b)
			return len(b), nil
		}
		p.bannerText = text
		if !IsBannerSuspect(text) {
			p.sawContent = true
			// The held bytes are the earlier half of this answer: append and
			// let the commit relay the whole frame, so the client receives it
			// byte for byte instead of starting mid-JSON.
			p.buf.Write(b)
			p.heldContent = false
			p.commitLocked()
			return len(b), nil
		}
		p.buf.Write(b)
		return len(b), nil
	}

	// A keep-alive, a [DONE], or a role-only frame after the turn already
	// committed: nothing to hold. The buffer must not carry preamble into the
	// commit — it would be relayed as if it were answer content.
	if p.sawContent {
		p.buf.Reset()
	}
	p.buf.Write(b)
	return len(b), nil
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
