package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/constants"
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

	// Forward the upstream's retry and rate-limit headers before any branch
	// below writes a status: they tell the client when it is worth retrying,
	// and dropping them turns a throttle into an opaque failure. Applied on
	// every response path, like upstream upstreamResponseHeaders.
	forwardUpstreamResponseHeaders(w, resp.Header)

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
		raw, rerr := io.ReadAll(io.LimitReader(resp.Body, constants.MaxUpstreamBodyBytes))
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
	return sseStream(sseStreamOpts{
		W: w, Upstream: upstream, Translate: req.TranslateResp, StartTime: startTime,
		TTFT: req.TTFT, Buf: req.ResponseBuf, Ctx: req.Ctx, ToolNameMap: req.ToolNameMap,
	})
}

// sseStreamOpts bundles sseStream inputs. Eight positional params (an
// io.Reader next to an io.Writer, two adjacent time/TTFT values) made call
// sites unreadable; named fields fix the call sites while the function body
// intentionally keeps short local aliases.
type sseStreamOpts struct {
	W           http.ResponseWriter
	Upstream    io.Reader
	Translate   bool
	StartTime   time.Time
	TTFT        *int64
	Buf         io.Writer
	Ctx         context.Context
	ToolNameMap map[string]string
}

// sseStream pipes SSE chunks to client with optional format translation.
func sseStream(o sseStreamOpts) error {
	w, upstream := o.W, o.Upstream
	translate, startTime := o.Translate, o.StartTime
	ttft, buf := o.TTFT, o.Buf
	ctx, toolNameMap := o.Ctx, o.ToolNameMap

	// A /v1/responses client on a Chat Completions upstream needs the answer
	// replayed as Responses events. The branch sits ahead of the header write
	// because the bridge owns its own writer, and ahead of the Claude branch
	// because translateResponse describes a Claude client, which a Responses
	// client never sets.
	if translator.NeedsResponsesBridge(ctx) {
		return streamChatToResponses(o)
	}

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
			// Truncated or mid-stream aborted upstream. HTTP 200 is already
			// on the wire, so report the failure in-band: openai-python
			// raises on a `data:` payload carrying `error` rather than
			// keeping the truncated text (upstream 93001213).
			if err != nil || !sawTerminal {
				code, message := proxy.ClassifyStreamAbort(err)
				_, _ = hw.Write(proxy.BuildStreamErrorBytes(code, message, proxy.SSEFormatClaude))
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
			captureUsageFromSSEChunk(ctx, chunk)
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

func captureUsageFromSSEChunk(ctx context.Context, chunk []byte) {
	const maxUsageFrame = 1 << 20
	trimmed := bytes.TrimSpace(chunk)
	if len(trimmed) > maxUsageFrame {
		trimmed = trimmed[:maxUsageFrame]
	}
	if !bytes.Contains(trimmed, []byte("data:")) {
		return
	}
	_ = proxy.ScanStream(bytes.NewReader(trimmed), func(payload []byte) {
		if bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
			return
		}
		if usage := translator.ParseResponseUsage(payload); usage != nil {
			translator.SetUsage(ctx, usage)
		}
	})
}

// jsonResponse writes the upstream JSON response with optional translation.
func jsonResponse(ctx context.Context, w http.ResponseWriter, upstream io.Reader, translate bool, buf io.Writer) error {
	body, err := io.ReadAll(io.LimitReader(upstream, constants.MaxUpstreamBodyBytes))
	if err != nil {
		return fmt.Errorf("read upstream response: %w", err)
	}

	body = translator.UnwrapClineEnvelope(body)

	// An SSE-only upstream ignores `stream:false` and answers with an event
	// stream anyway. Writing that under an `application/json` header hands the
	// client text its JSON.parse cannot read, so fold the stream into one
	// chat.completion first. A body whose first line is not `data:`/`event:`
	// is not SSE and is left exactly as it was.
	if looksLikeSSE(body) {
		folded, ok := sseToOpenAIJSON(body)
		if !ok {
			// SSE-shaped but carrying no completion chunk. Relabelling it as
			// JSON would repeat the bug, and a 502 lets combo fallback move
			// on to the next account.
			return sseWithoutCompletionError("upstream answered with an event stream containing no completion")
		}
		body = folded
	}

	if buf != nil {
		buf.Write(body)
	}

	// A /v1/responses client on a Chat Completions upstream needs the answer in
	// the Responses shape; translate marks a Claude client, which it is not.
	// The raw Chat body still went to the log buffer and to usage parsing,
	// exactly as it does for the Claude path.
	if translator.NeedsResponsesBridge(ctx) {
		return jsonResponseAsResponses(ctx, w, body)
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

// looksLikeSSE reports whether a non-streaming body is actually an event
// stream. Only the first non-empty line decides: a JSON body starts with `{`,
// so requiring an SSE field prefix there cannot misfire on one.
func looksLikeSSE(body []byte) bool {
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		return strings.HasPrefix(trimmed, "data:") || strings.HasPrefix(trimmed, "event:")
	}
	return false
}

// sseDataFrames returns the JSON payload of every `data:` frame.
func sseDataFrames(body []byte) [][]byte {
	var frames [][]byte
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "data:") {
			continue
		}
		payload := strings.TrimSpace(trimmed[5:])
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			continue
		}
		frames = append(frames, []byte(payload))
	}
	return frames
}

// sseWithoutCompletionError reports an event stream that carried no
// completion chunk. Answering 200 with an empty body reads as a successful
// empty completion and silently ends combo fallback, so this is a 502 the
// fallback layer can act on.
func sseWithoutCompletionError(message string) error {
	body, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "upstream_error",
			"code":    http.StatusBadGateway,
		},
	})
	return &proxy.UpstreamError{StatusCode: http.StatusBadGateway, Body: body}
}
