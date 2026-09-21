package executor

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"time"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/log"
	"9router/proxy/internal/proxy"
	"9router/proxy/internal/translator"
)

// handleClaudeMessagesStream pipes a Claude Messages SSE stream from upstream,
// translating chunks to OpenAI SSE format when the downstream client is an OpenAI client.
func handleClaudeMessagesStream(w http.ResponseWriter, req *Request, upstream io.Reader) error {
	if req.TranslateResp {
		// Client requested Claude format (/v1/messages) and upstream is already Claude format.
		// Stream directly via SSECopy without format translation.
		startTime := req.StartTime
		if startTime.IsZero() {
			startTime = time.Now()
		}
		return sseStream(w, upstream, false, startTime, req.TTFT, req.ResponseBuf, req.Ctx, req.ToolNameMap)
	}

	// Client requested OpenAI format (/v1/chat/completions) but upstream is Claude Messages SSE.
	// Translate each Claude SSE event into standard OpenAI chunk SSE (choices[0].delta).
	hw := proxy.NewHeartbeatWriter(req.Ctx, w, 0)
	defer hw.Close()
	flusher := proxy.WriteSSEHeaders(hw)

	state := &translator.ClaudeToOpenAIStreamState{}
	doneSeen := false
	sawTerminal := false // saw message_delta (with stop_reason) or message_stop
	decloaker := NewClaudeStreamDecloaker(req.ToolNameMap)
	var writeErr error
	emit := func(payload []byte) {
		if doneSeen || writeErr != nil {
			return
		}
		if bytes.Contains(payload, []byte(`"message_delta"`)) || bytes.Contains(payload, []byte(`"message_stop"`)) {
			sawTerminal = true
		}
		trimmed := bytes.TrimSpace(payload)
		if string(trimmed) == "[DONE]" {
			doneSeen = true
			_, _ = hw.Write([]byte("data: [DONE]\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
			return
		}

		out, terr := translator.TranslateClaudeChunkToOpenAI(payload, state)
		if terr != nil {
			log.Error("executor", "translate claude chunk to openai", "error", terr)
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
			req.ResponseBuf.Write(out)
		}
		if _, werr := hw.Write(out); werr != nil {
			// Client went away mid-stream: stop feeding it and report the
			// abort instead of recording a 200.
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
	err := proxy.ScanStream(upstream, func(payload []byte) {
		if doneSeen || writeErr != nil {
			return
		}
		if decloaker != nil {
			for _, ev := range decloaker.Events(payload) {
				emit(ev.Payload)
			}
			return
		}
		emit(payload)
	})
	if writeErr != nil {
		return fmt.Errorf("write to client: %w", writeErr)
	}

	if !doneSeen {
		// Truncated or mid-stream aborted upstream: mirror SSECopy's terminal
		// synthesis (PR #4079) so clients like Oh My Pi get an explicit
		// finish_reason instead of "stream closed before finish_reason".
		if err != nil || !sawTerminal {
			term := []byte(`data: {"id":` + mustJSONString(state.MessageID) + `,"object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"network_error"}]}` + "\n\n")
			if _, werr := hw.Write(term); werr == nil && flusher != nil {
				flusher.Flush()
			}
		}
		_, _ = hw.Write([]byte("data: [DONE]\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}
	return err
}

// handleClaudeMessagesNonStream handles non-streaming Claude Messages responses,
// translating Claude JSON to OpenAI JSON when the downstream client is an OpenAI client.
func handleClaudeMessagesNonStream(w http.ResponseWriter, req *Request, upstream io.Reader) error {
	body, err := io.ReadAll(io.LimitReader(upstream, constants.MaxUpstreamBodyBytes))
	if err != nil {
		return fmt.Errorf("read claude response body: %w", err)
	}

	if req.TranslateResp {
		// Client requested Claude format, upstream is Claude format: pass through
		return jsonResponse(req.Ctx, w, bytes.NewReader(body), false, req.ResponseBuf)
	}

	if req.ToolNameMap != nil {
		// Claude OAuth tool cloaking: restore original tool names before
		// translating / forwarding the response to the client.
		body = DecloakClaudeResponseBody(body, req.ToolNameMap)
	}

	// Client requested OpenAI format, upstream is Claude format: translate!
	converted, err := translator.TranslateClaudeResponseToOpenAI(body)
	if err != nil {
		return fmt.Errorf("translate claude response to openai: %w", err)
	}
	return jsonResponse(req.Ctx, w, bytes.NewReader(converted), false, req.ResponseBuf)
}
