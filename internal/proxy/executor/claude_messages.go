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
	// Built before either branch returns: a /v1/messages client on a Claude
	// upstream still needs fitted tool names restored, and the TranslateResp
	// passthrough below used to skip the decloaker entirely, forwarding the
	// fitted name so the client could not dispatch the call.
	decloaker := NewClaudeStreamDecloaker(req.ToolNameMap)

	if req.TranslateResp {
		startTime := req.StartTime
		if startTime.IsZero() {
			startTime = time.Now()
		}
		hw := proxy.NewHeartbeatWriter(req.Ctx, w, 0)
		defer hw.Close()
		flusher := proxy.WriteSSEHeaders(hw)
		writeFrame := func(payload []byte) {
			if req.TTFT != nil && *req.TTFT == 0 {
				*req.TTFT = time.Since(startTime).Milliseconds()
			}
			frame := append([]byte("data: "), payload...)
			frame = append(frame, '\n', '\n')
			if req.ResponseBuf != nil {
				req.ResponseBuf.Write(frame)
			}
			_, _ = hw.Write(frame)
			if flusher != nil {
				flusher.Flush()
			}
			raw := append([]byte(`{"message":`), payload...)
			raw = append(raw, '}')
			if usage := translator.ParseClaudeUsage(raw); usage != nil {
				translator.SetUsage(req.Ctx, usage)
			}
		}
		return proxy.ScanStream(upstream, func(payload []byte) {
			if decloaker != nil {
				for _, ev := range decloaker.Events(payload) {
					writeFrame(ev.Payload)
				}
				return
			}
			writeFrame(payload)
		})
	}

	// Client requested OpenAI format (/v1/chat/completions) but upstream is Claude Messages SSE.
	// Translate each Claude SSE event into standard OpenAI chunk SSE (choices[0].delta).
	hw := proxy.NewHeartbeatWriter(req.Ctx, w, 0)
	defer hw.Close()
	flusher := proxy.WriteSSEHeaders(hw)

	// A /v1/responses client is one hop further out: the Claude events become
	// OpenAI chunks here and those chunks become Responses events, so the
	// bridge consumes this branch's output instead of the raw writer.
	var bridge *ResponsesBridge
	if translator.NeedsResponsesBridge(req.Ctx) {
		startTime := req.StartTime
		if startTime.IsZero() {
			startTime = time.Now()
		}
		bridge = NewResponsesBridge(
			translator.RequestedModelFromContext(req.Ctx),
			translator.CustomToolNamesFrom(req.Ctx),
			responsesWriter(sseStreamOpts{TTFT: req.TTFT, Buf: req.ResponseBuf}, hw, flusher, startTime),
		)
	}

	// A Messages stream that omits `model` still has to echo one back, and
	// the model the client asked for is the value that is actually true. The
	// translator's own fallback is a placeholder, not a substitute for this.
	state := &translator.ClaudeToOpenAIStreamState{
		Model: translator.RequestedModelFromContext(req.Ctx),
	}
	doneSeen := false
	sawTerminal := false // saw message_delta (with stop_reason) or message_stop
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

		if bridge != nil {
			bridge.FeedFrames(out)
			if bridge.err != nil {
				writeErr = bridge.err
			}
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
	if state.Usage != nil {
		translator.SetUsage(req.Ctx, state.Usage)
	}
	if writeErr != nil {
		return fmt.Errorf("write to client: %w", writeErr)
	}

	// The upstream is fully drained here, so the completion watchdog has nothing
	// left to give up on. Cancelling it before Close keeps the timer from
	// firing into a writer this function is about to tear down.
	if bridge != nil {
		bridge.StopCompletionWatchdog()
	}

	// A Responses client waits for response.completed, not [DONE], so the bridge
	// closes before the fallback below can call the stream unfinished.
	if bridge != nil {
		bridge.Close()
		doneSeen = true
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

	// Decloak before either return. The passthrough is not exempt: a
	// /v1/messages client still needs the caller's original tool name back, and
	// restoring after the translate branch would miss every Claude-native body.
	if req.ToolNameMap != nil {
		// Claude OAuth tool cloaking: restore original tool names before
		// translating / forwarding the response to the client.
		body = DecloakClaudeResponseBody(body, req.ToolNameMap)
	}

	if req.TranslateResp {
		// Client requested Claude format, upstream is Claude format: pass through
		return jsonResponse(req.Ctx, w, bytes.NewReader(body), false, req.ResponseBuf)
	}

	// Client requested OpenAI format, upstream is Claude format: translate!
	converted, err := translator.TranslateClaudeResponseToOpenAI(body)
	if err != nil {
		return fmt.Errorf("translate claude response to openai: %w", err)
	}
	return jsonResponse(req.Ctx, w, bytes.NewReader(converted), false, req.ResponseBuf)
}
