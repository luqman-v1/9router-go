package executor

// Response translation for Zed's google (Gemini) provider.
//
// Zed's google provider answers in the Gemini dialect: a "candidates" array
// with "content.parts", plus "usageMetadata". The repository's Gemini
// translator already turns that into OpenAI chat chunks, so the work here is
// the plumbing: the framing (Zed speaks JSON Lines, see zed_stream.go) and
// the stream lifecycle the OpenAI client expects.

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"time"

	"9router/proxy/internal/proxy"
	"9router/proxy/internal/translator"
)

// zedGeminiStream translates a Zed google stream into OpenAI chat SSE.
func zedGeminiStream(w http.ResponseWriter, req *Request, upstream io.Reader) error {
	hw := proxy.NewHeartbeatWriter(req.Ctx, w, 0)
	defer func() { _ = hw.Close() }()
	flusher := proxy.WriteSSEHeaders(hw)

	state := &translator.GeminiStreamState{
		Model: translator.RequestedModelFromContext(req.Ctx),
	}
	doneSeen := false
	var writeErr error

	emit := func(payload []byte) {
		if doneSeen || writeErr != nil {
			return
		}
		trimmed := bytes.TrimSpace(payload)
		if len(trimmed) == 0 {
			return
		}
		if string(trimmed) == "[DONE]" {
			doneSeen = true
			_, _ = hw.Write([]byte("data: [DONE]\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
			return
		}
		out, terr := translator.TranslateGeminiChunkToOpenAI(trimmed, state)
		if terr != nil {
			writeErr = fmt.Errorf("translate gemini chunk to openai: %w", terr)
			return
		}
		if len(out) == 0 {
			return
		}
		if req.TTFT != nil && *req.TTFT == 0 {
			startTime := req.StartTime
			if startTime.IsZero() {
				startTime = time.Now()
			}
			*req.TTFT = time.Since(startTime).Milliseconds()
		}
		if req.ResponseBuf != nil {
			_, _ = req.ResponseBuf.Write(out)
		}
		if _, werr := hw.Write(out); werr != nil {
			writeErr = werr
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
		if bytes.Contains(out, []byte("[DONE]")) {
			doneSeen = true
		}
	}

	err := proxy.ScanStream(upstream, emit)
	if state.Usage != nil {
		translator.SetUsage(req.Ctx, state.Usage)
	}
	if writeErr != nil {
		return writeErr
	}
	if !doneSeen {
		_, _ = hw.Write([]byte("data: [DONE]\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}
	return err
}

// zedGeminiWhole answers a non-streaming client from a complete Gemini body.
func zedGeminiWhole(w http.ResponseWriter, req *Request, data []byte) error {
	converted, usage, err := translator.TranslateGeminiResponseToOpenAI(data)
	if err != nil {
		return fmt.Errorf("translate gemini response to openai: %w", err)
	}
	if usage != nil {
		translator.SetUsage(req.Ctx, usage)
	}
	return jsonResponse(req.Ctx, w, bytes.NewReader(converted), req.TranslateResp, req.ResponseBuf)
}
