package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/guardrails"
	"9router/proxy/internal/log"
	"9router/proxy/internal/proxy"
	"9router/proxy/internal/shutdown"
	"9router/proxy/internal/translator"
	"encoding/json/jsontext"
	json "encoding/json/v2"
)

// sseSniffBytes bounds the head read that decides a mislabelled stream: SSE
// always opens with a `data:`/`event:` field line, so the first kilobyte is
// verdict enough and the rest of the stream stays live.
const sseSniffBytes = 1024

// sniffStreamHead reads the head of a live body: it stops as soon as one
// field line has arrived (an SSE body announces itself in its first field
// line) or the cap is reached, without waiting for more bytes than the peer
// has sent. A plain io.ReadAll(LimitReader(...)) would block here until the
// cap fills or the stream ends — exactly the buffering this sniff exists to
// avoid.
//
// Leading blank lines are skipped rather than ending the sniff. A provider
// that flushes an empty keepalive line before its first frame is still a
// stream, and a verdict taken on that blank line folds the whole turn into a
// single document — a client that asked for a stream then reads one.
func sniffStreamHead(r io.Reader) []byte {
	buf := make([]byte, sseSniffBytes)
	head := make([]byte, 0, sseSniffBytes)
	for len(head) < sseSniffBytes {
		n, rerr := r.Read(buf[len(head):])
		head = append(head, buf[len(head):len(head)+n]...)
		if rerr != nil || n == 0 {
			break
		}
		if bytes.IndexByte(head[firstLineStart(head):], '\n') >= 0 {
			break
		}
	}
	return head
}

// firstLineStart returns the offset of the first line in head that carries
// something, so the sniff reads past leading blank lines instead of deciding
// on one. SSE frame separators are blank lines, and a chunked provider may
// flush one before its first field.
func firstLineStart(head []byte) int {
	for i := range head {
		switch head[i] {
		case '\n':
			continue
		case '\r':
			// A trailing CR is undecided until the next byte arrives: the
			// sniff reads on rather than judging the line.
			if i+1 == len(head) || head[i+1] == '\n' {
				continue
			}
			return i
		default:
			return i
		}
	}
	return len(head)
}

// replayBody replays an already-read head ahead of a live upstream body while
// keeping that body's Close. A NopCloser here would leave the socket
// unclosable: the stall watchdog ends a stuck stream by closing the reader,
// and the deferred close releases the connection, so both have to reach the
// upstream body rather than a wrapper that swallows the close.
type replayBody struct {
	io.Reader
	io.Closer
}

// renameMaxTokensBody moves max_tokens onto max_completion_tokens and drops
// the old key, keeping an explicit client-sent target value. A body without
// max_tokens, or one that does not parse as an object, returns unchanged.
func renameMaxTokensBody(body []byte) []byte {
	var m map[string]jsontext.Value
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	raw, ok := m["max_tokens"]
	if !ok {
		return body
	}
	if _, exists := m["max_completion_tokens"]; !exists {
		m["max_completion_tokens"] = raw
	}
	delete(m, "max_tokens")
	out, err := marshalStable(m)
	if err != nil {
		return body
	}
	return out
}

// ForwardOpenAI sends an OpenAI-format request and writes the response.
func ForwardOpenAI(w http.ResponseWriter, req *Request) error {
	// Provider-scoped param rules from upstream paramSupport strip unsupported
	// params on the outgoing body (executors/default.js:91). The openai rule
	// renames max_tokens because current OpenAI models answer 400 "Use
	// 'max_completion_tokens' instead".
	if req.Provider == "openai" {
		req.Body = renameMaxTokensBody(req.Body)
	}
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
		streamBody := io.ReadCloser(resp.Body)
		if !isEventStreamResponse(resp.Header) {
			// A 200 that is not an event stream is usually one document, not
			// a stream. Piping it through the SSE scanner yields zero frames,
			// SSECopy closes it with [DONE], and the client reads a
			// successful empty completion while the router never gets a
			// chance to fail over. A bounded head sniff decides instead of a
			// whole-body read: SSE always announces itself in the first
			// field lines, so a provider that mislabels its stream as JSON
			// still streams frame by frame, and only a genuine document (an
			// HTML error page, a blank 200) is read whole and validated as
			// JSON.
			head := sniffStreamHead(resp.Body)
			if proxy.LooksLikeSSE(head) {
				// An event stream the provider mislabelled: hand the sniffed
				// head back and let the rest stream live. The upstream body
				// stays the Closer so the stall watchdog and the deferred
				// close can still reach the socket.
				streamBody = replayBody{Reader: io.MultiReader(bytes.NewReader(head), resp.Body), Closer: resp.Body}
			} else {
				rest, rerr := io.ReadAll(io.LimitReader(resp.Body, constants.MaxUpstreamBodyBytes-int64(len(head))))
				if rerr != nil {
					return fmt.Errorf("read upstream body: %w", rerr)
				}
				return jsonResponse(req.Ctx, w, bytes.NewReader(append(head, rest...)), req.TranslateResp, req.ResponseBuf)
			}
		}
		stallReader := proxy.NewStallReaderWithContext(req.Ctx, streamBody, 0, "openai")
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
		LiftReasoningAliases: req.LiftReasoningAliases,
	})
}

// isEventStreamResponse reports whether an upstream response is an event
// stream. A missing Content-Type is not one: the body decides.
func isEventStreamResponse(h http.Header) bool {
	return strings.HasPrefix(strings.ToLower(h.Get("Content-Type")), "text/event-stream")
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
	// LiftReasoningAliases patches passthrough deltas to expose alias-shaped
	// thinking as reasoning_content (opencode quirk).
	LiftReasoningAliases bool
}

// sseStream pipes SSE chunks to client with optional format translation.
func sseStream(o sseStreamOpts) error {
	w, upstream := o.W, o.Upstream
	translate, startTime := o.Translate, o.StartTime
	ttft, buf := o.TTFT, o.Buf
	ctx, toolNameMap := o.Ctx, o.ToolNameMap
	liftAliases := o.LiftReasoningAliases

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
					if liftAliases {
						ev.Payload = liftReasoningAliasesPayload(ev.Payload)
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
		var patchers []func([]byte) []byte
		if liftAliases {
			patchers = append(patchers, liftReasoningAliasesPayload)
		}
		return proxy.SSECopy(hw, upstream, flusher, func(chunk []byte) {
			if ttft != nil && *ttft == 0 {
				*ttft = time.Since(startTime).Milliseconds()
			}
			if buf != nil {
				buf.Write(chunk)
			}
			captureUsageFromSSEChunk(ctx, chunk)
		}, patchers...)
	}

	sessionKey := translator.NewSessionKey("stream")
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
		usage := translator.ParseResponseUsage(payload)
		if usage == nil {
			usage = translator.ParseResponsesUsage(payload)
		}
		if usage != nil {
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
	if proxy.LooksLikeSSE(body) {
		folded, ok := sseToOpenAIJSON(body)
		if !ok {
			// SSE-shaped but carrying no completion chunk. Relabelling it as
			// JSON would repeat the bug, and a 502 lets combo fallback move
			// on to the next account.
			return proxy.UpstreamFailure(http.StatusBadGateway, proxy.NoCompletionInStream)
		}
		body = folded
	}

	if armed, canFallback := proxy.ZeroContentProbe(ctx); armed && proxy.IsZeroContentResponse(body) {
		if canFallback {
			return &proxy.ZeroContentError{Body: body}
		}
		// The last member of a combo serves its zero-content response normally (FR3).
	} else if err := proxy.EmptyUpstreamError(body); err != nil {
		return err
	}

	// Outbound guardrails. The body is whole in hand and nothing has been
	// written yet, so a block can still become a real status code rather than
	// a cut stream, and a mask can rewrite the answer before any of it is
	// relayed. The policy travels on the context, which every executor already
	// carries, so this covers every provider without threading an engine
	// through every call site that ends up here.
	filtered, gerr := guardrails.ApplyBuffered(ctx, body)
	if gerr != nil {
		return guardrailBlockFailure(gerr)
	}
	body = filtered

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
			// A Claude client cannot read a Chat Completions body. Serving it
			// under a 200 hands the client JSON that is not a Message — the
			// "HTTP 200 … body is JSON but not a Message" report. Fail as an
			// upstream error so the router can fall back, same as the retired
			// chat path (handlers/chat/forward.go).
			return proxy.UpstreamFailure(http.StatusBadGateway,
				"upstream response could not be translated to Claude format")
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
