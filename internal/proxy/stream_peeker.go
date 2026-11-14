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
	buf            bytes.Buffer
	header         http.Header
	statusCode     int
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

	if HasSSEContent(b) {
		p.sawContent = true
		p.commitLocked()
		return p.ResponseWriter.Write(b)
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
		return ErrStreamingZeroContent
	}
	if !p.committed {
		p.commitLocked()
	}
	return nil
}
