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

// HasSSEContent reports whether an SSE chunk carries client-visible assistant content.
// Delta role, reasoning/thinking, and empty finish frames do not count as content.
func HasSSEContent(chunk []byte) bool {
	if len(chunk) == 0 {
		return false
	}
	s := string(chunk)
	// OpenAI chat completions
	if strings.Contains(s, `"content":"`) {
		// Ensure it's not empty content: ""
		if !strings.Contains(s, `"content":""`) && !strings.Contains(s, `"content":null`) {
			return true
		}
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
	if strings.Contains(s, `"type":"content_block_start"`) && strings.Contains(s, `"type":"tool_use"`) {
		return true
	}
	// Responses API
	if strings.Contains(s, `"response.output_text.delta"`) || strings.Contains(s, `"response.output_item.added"`) {
		return true
	}
	return false
}

// StreamContentPeeker buffers initial SSE frames until content is observed.
type StreamContentPeeker struct {
	http.ResponseWriter
	flusher    http.Flusher
	mu         sync.Mutex
	committed  bool
	sawContent bool
	buf        bytes.Buffer
	header     http.Header
	statusCode int
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
	if _, _, isErr := DetectInbandSSEError(b); isErr || bytes.Contains(b, []byte(`"error":`)) {
		p.sawContent = true
		p.commitLocked()
		return p.ResponseWriter.Write(b)
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

	if !p.sawContent {
		return ErrStreamingZeroContent
	}
	if !p.committed {
		p.commitLocked()
	}
	return nil
}
