package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"9router/proxy/internal/log"
	"9router/proxy/internal/proxy"
	"9router/proxy/internal/shutdown"
	"9router/proxy/internal/translator"
)

// ForwardOpenAI sends an OpenAI-format request and writes the response.
func ForwardOpenAI(w http.ResponseWriter, req *Request) error {
	resp, err := proxy.ForwardOpenAI(req.Ctx, req.Client, req.Config, req.APIKey, req.Body, req.IsStream)
	if err != nil {
		return fmt.Errorf("ForwardOpenAI upstream: %w", err)
	}

	var bodyCloser io.Closer = resp.Body
	defer func() {
		if bodyCloser != nil {
			bodyCloser.Close()
		}
	}()

	if req.IsStream {
		stallReader := proxy.NewStallReaderWithContext(req.Ctx, resp.Body, 0, "openai")
		bodyCloser = stallReader
		if req.UpstreamClaude {
			// Upstream is Claude Messages, client is OpenAI (/v1/chat/completions):
			// translate the response instead of passing Claude SSE through.
			return handleClaudeMessagesStream(w, req, stallReader)
		}
		return execSSEStream(w, stallReader, req)
	}
	if req.UpstreamClaude {
		return handleClaudeMessagesNonStream(w, req, resp.Body)
	}
	if req.ToolNameMap != nil {
		// Claude OAuth tool cloaking: restore original tool names before
		// the response reaches the client.
		raw, rerr := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
		if rerr != nil {
			return fmt.Errorf("read upstream body: %w", rerr)
		}
		decloaked := DecloakClaudeResponseBody(raw, req.ToolNameMap)
		return jsonResponse(req.Ctx, w, bytes.NewReader(decloaked), req.TranslateResp, req.ResponseBuf)
	}
	return jsonResponse(req.Ctx, w, resp.Body, req.TranslateResp, req.ResponseBuf)
}

func execSSEStream(w http.ResponseWriter, upstream io.Reader, req *Request) error {
	startTime := req.StartTime
	if startTime.IsZero() {
		startTime = time.Now()
	}
	return sseStream(w, upstream, req.TranslateResp, startTime, req.TTFT, req.ResponseBuf, req.Ctx, req.ToolNameMap)
}

// sseStream pipes SSE chunks to client with optional format translation.
func sseStream(w http.ResponseWriter, upstream io.Reader, translate bool, startTime time.Time, ttft *int64, buf io.Writer, ctx context.Context, toolNameMap map[string]string) error {
	hw := proxy.NewHeartbeatWriter(ctx, w, 0)
	defer hw.Close()
	flusher := proxy.WriteSSEHeaders(hw)

	if !translate {
		if toolNameMap != nil {
			decloaker := NewClaudeStreamDecloaker(toolNameMap)
			var writeErr error
			doneSent := false
			sawTerminal := false // saw message_delta (with stop_reason) or message_stop
			err := proxy.ScanStream(upstream, func(chunk []byte) {
				if writeErr != nil || doneSent {
					return
				}
				events := decloaker.Events(chunk)
				for _, ev := range events {
					if len(ev.Payload) == 0 {
						continue
					}
					if bytes.Contains(ev.Payload, []byte(`"message_delta"`)) || bytes.Contains(ev.Payload, []byte(`"message_stop"`)) {
						sawTerminal = true
					}
					if bytes.Equal(bytes.TrimSpace(ev.Payload), []byte("[DONE]")) {
						line := fmt.Appendf(nil, "data: [DONE]\n\n")
						if _, werr := hw.Write(line); werr != nil {
							writeErr = werr
						}
						if flusher != nil {
							flusher.Flush()
						}
						doneSent = true
						return
					}
					if ttft != nil && *ttft == 0 {
						*ttft = time.Since(startTime).Milliseconds()
					}
					if buf != nil {
						buf.Write(ev.Payload)
					}
					var line []byte
					if ev.Type != "" {
						line = fmt.Appendf(nil, "event: %s\ndata: %s\n\n", ev.Type, string(ev.Payload))
					} else {
						line = fmt.Appendf(nil, "data: %s\n\n", string(ev.Payload))
					}
					if _, werr := hw.Write(line); werr != nil {
						// Client went away mid-stream: stop feeding it and report
						// the abort instead of recording a 200.
						writeErr = werr
						return
					}
					if flusher != nil {
						flusher.Flush()
					}
				}
			})
			if writeErr != nil {
				return fmt.Errorf("write to client: %w", writeErr)
			}
			// Truncated or mid-stream aborted upstream: synthesize a terminal
			// error event (mirrors SSECopy's finish_reason synthesis, PR #4079)
			// so native clients do not hang on a stream with no end.
			if err != nil || !sawTerminal {
				term := "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"upstream stream ended before completion\"}}\n\n"
				_, _ = hw.Write([]byte(term))
				if flusher != nil {
					flusher.Flush()
				}
			}
			return err
		}
		return proxy.SSECopy(hw, upstream, flusher, func(chunk []byte) {
			if ttft != nil && *ttft == 0 {
				*ttft = time.Since(startTime).Milliseconds()
			}
			if buf != nil {
				buf.Write(chunk)
			}
		})
	}

	sessionKey := fmt.Sprintf("stream-%d", time.Now().UnixNano())
	defer translator.ClearStreamState(sessionKey)
	finished := false
	err := proxy.ScanStream(upstream, func(chunk []byte) {
		translated, err := translator.TranslateOpenAIToClaudeStreamSession(sessionKey, chunk)
		if err != nil {
			log.Error("executor", "translate error", "error", err)
			return
		}
		if translated == nil {
			return
		}
		if bytes.Contains(translated, []byte("[DONE]")) {
			finished = true
		}
		if ttft != nil && *ttft == 0 {
			*ttft = time.Since(startTime).Milliseconds()
		}
		if buf != nil {
			buf.Write(translated)
		}
		hw.Write(translated)
		if flusher != nil {
			flusher.Flush()
		}
	})
	// Same shutdown terminator as the chat path: end with [DONE] on abort.
	if shutdown.Fired() && !finished {
		hw.Write([]byte("data: [DONE]\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}
	// Pull actual accumulated usage (incl. cached tokens) out of the session so
	// the log sees real numbers instead of the fallback estimate.
	if usage := translator.GetStreamUsage(sessionKey); usage != nil {
		translator.SetUsage(ctx, usage)
	}
	return err
}

// jsonResponse writes the upstream JSON response with optional translation.
func jsonResponse(ctx context.Context, w http.ResponseWriter, upstream io.Reader, translate bool, buf io.Writer) error {
	body, err := io.ReadAll(io.LimitReader(upstream, 10*1024*1024))
	if err != nil {
		return fmt.Errorf("read upstream response: %w", err)
	}

	body = translator.UnwrapClineEnvelope(body)

	if buf != nil {
		buf.Write(body)
	}

	if translate {
		translated, usage, err := translator.TranslateOpenAIToClaude(body)
		if err == nil && usage != nil {
			if ctx != nil {
				translator.SetUsage(ctx, usage)
			} else {
				translator.SetLastUsage(usage)
			}
		}
		if err != nil || translated == nil {
			log.Error("executor", "json translate error", "error", err)
			// Fall back to original response
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write(body)
			return nil
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(translated)
		return nil
	}

	if usage := translator.ParseResponseUsage(body); usage != nil {
		if ctx != nil {
			translator.SetUsage(ctx, usage)
		} else {
			translator.SetLastUsage(usage)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(body)
	return nil
}
