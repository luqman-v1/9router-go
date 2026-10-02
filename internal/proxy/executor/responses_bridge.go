package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
	"9router/proxy/internal/translator"
)

// UpstreamSpeaksResponses reports whether the request is served by an OpenAI
// Responses endpoint, in which case a /v1/responses client and the upstream
// already agree on the wire format and neither direction may be translated.
// Upstream makes the same call by comparing the source format against the
// transport's own format (chatCore resolveTransport + translateRequest).
//
// Two shapes count as Responses-native, both read off data the gateway already
// carries rather than a new table:
//
//   - a base URL that is the /responses endpoint itself (codex, grok-cli,
//     perplexity-agent) — the same test the opencode executors use before
//     appending /responses to their chat base URL;
//   - an opencode model whose declared target format is the Responses lane
//     (muse-spark, grok-4.6, gpt-5.6-luna) — a chat-completions or Claude
//     client asking for the same model is translated in, not relayed.
func UpstreamSpeaksResponses(provider, model string, cfg *providers.ProviderConfig) bool {
	if cfg != nil && strings.HasSuffix(strings.TrimRight(cfg.BaseURL, "/"), "/responses") {
		return true
	}
	switch provider {
	case "opencode", "opencode-go":
		formats, declared := providers.GetModelFormats(provider, model)
		if !declared {
			return isOpencodeResponsesModel(cleanResponsesModel(model))
		}
		// Only a Responses-native lane stays on /responses. A model that has
		// to be translated into Responses (a Chat client asking for
		// deepseek-v4-pro) must not be relayed as if the upstream already
		// spoke the client's format.
		return formats.TargetFormat == providers.FormatOpenAIResponses
	case "opencode-zen":
		// Muse Spark is the only lane that speaks Responses natively, and
		// only for an id the catalog knows — an undeclared id keeps the
		// family fallback the executor applies.
		formats, declared := providers.GetModelFormats(provider, cleanResponsesModel(model))
		return declared && formats.TargetFormat == providers.FormatOpenAIResponses
	case "muse":
		// Meta's Model API serves both lanes on one key, and every Muse Spark
		// model pins the Responses one, so a /v1/responses client is relayed
		// without a round trip through Chat Completions.
		return true
	default:
		return false
	}
}

// ResponsesBridge replays an upstream Chat Completions stream as the Responses
// events a /v1/responses client expects. It accepts both a single SSE payload
// and a buffer of ready-made "data: {...}" frames, so it can sit behind any
// Chat producer — the plain OpenAI stream, the Claude-to-OpenAI translation, or
// the Gemini-to-OpenAI translation — without any of them knowing it exists.
type ResponsesBridge struct {
	// mu guards the translation state against the watchdog timer, which emits
	// from its own goroutine when the upstream stalls.
	mu       sync.Mutex
	state    *translator.ResponsesState
	write    func([]byte) error
	finished bool
	err      error
	watchdog *completionWatchdog
	// completionPending mirrors state.CompletionPending as an atomic. The scan
	// reads it between every line of the upstream, so taking the lock there
	// would make the watchdog's own emission wait on a lock it needs to break
	// the stall. feed is the only writer and always holds mu.
	completionPending atomic.Bool
}

// NewResponsesBridge builds a bridge that writes Responses SSE frames through
// write. customToolNames marks the tools the client declared as freeform custom
// tools so their call arguments replay as custom_tool_call_input rather than as
// JSON arguments.
func NewResponsesBridge(model string, customToolNames []string, write func([]byte) error) *ResponsesBridge {
	state := translator.InitResponsesState(model, true)
	state.SetCustomToolNames(customToolNames)
	b := &ResponsesBridge{state: state, write: write}
	newCompletionWatchdog(b)
	return b
}

// Feed translates one upstream Chat Completions SSE payload. A payload that
// carries no JSON still has to reach the translator, because the terminal null
// chunk is what closes the open items and emits response.completed.
func (b *ResponsesBridge) Feed(payload []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.feed(payload)
}

// feed is Feed without the lock, for callers that already hold it. A nil
// payload flushes the stream, which is how Close and the watchdog both emit
// the terminal event.
func (b *ResponsesBridge) feed(payload []byte) {
	if b.err != nil || b.finished {
		return
	}
	if err := b.writeEvents(translator.TranslateOpenAIToResponses(parseChatChunk(payload), b.state)); err != nil {
		b.err = err
		return
	}
	b.finished = b.state.Completed
	// The answer is closed but the terminal event is waiting on a usage
	// trailer: start the deadline that stops the client waiting forever.
	b.completionPending.Store(b.state.CompletionPending && !b.state.Completed)
	b.watchdog.arm()
}

// CompletionPending reports that the terminal event is owed to the client and
// the watchdog is what will deliver it.
func (b *ResponsesBridge) CompletionPending() bool {
	return b.completionPending.Load()
}

// StopCompletionWatchdog cancels the deferred-completion deadline for a
// producer that has drained its upstream and is about to close the bridge.
// A timer left armed would fire into a writer the caller is tearing down.
func (b *ResponsesBridge) StopCompletionWatchdog() {
	if b == nil || b.watchdog == nil {
		return
	}
	b.watchdog.stop()
}

// FeedFrames translates a buffer of ready-made Chat Completions SSE frames
// ("data: {...}\n\n", possibly several back to back). The Claude translation
// emits its output in that shape rather than one payload at a time, so the
// bridge accepts both framings instead of each producer re-implementing it.
func (b *ResponsesBridge) FeedFrames(frames []byte) {
	for _, frame := range bytes.Split(frames, []byte("\n\n")) {
		frame = bytes.TrimSpace(frame)
		if len(frame) == 0 {
			continue
		}
		b.Feed(bytes.TrimSpace(bytes.TrimPrefix(frame, []byte("data:"))))
	}
}

// Close flushes whatever the upstream left open. A truncated stream must not
// swallow response.completed: without a terminal event the client waits forever
// for a turn that already ended.
func (b *ResponsesBridge) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	// The producer reached the end of the stream on its own, so the watchdog
	// has nothing left to give up on — and one of the two must cancel it.
	b.watchdog.stop()
	if b.err != nil || b.finished {
		return
	}
	b.finished = true
	if err := b.writeEvents(translator.FlushResponses(b.state)); err != nil {
		b.err = err
	}
}

func (b *ResponsesBridge) writeEvents(events []translator.ResponsesEvent) error {
	frame := make([]byte, 0, 512)
	for _, ev := range events {
		frame = append(frame, translator.FormatResponsesSSE(ev)...)
	}
	if len(frame) == 0 {
		return nil
	}
	return b.write(frame)
}

// Err reports the write error that stopped the bridge, if any. A producer that
// keeps feeding after a failed write would otherwise spin until the upstream
// stream ends.
func (b *ResponsesBridge) Err() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.err
}

// parseChatChunk decodes one upstream SSE data payload.
func parseChatChunk(payload []byte) *translator.OpenAIChunk {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("[DONE]")) {
		return nil
	}
	var chunk translator.OpenAIChunk
	if err := json.Unmarshal(trimmed, &chunk); err != nil {
		log.Warn("responses bridge", "skip unparsable chat chunk", "error", err)
		return nil
	}
	return &chunk
}

// streamChatToResponses pipes an upstream Chat Completions stream to a
// /v1/responses client as Responses events. It is the OpenAI-shaped twin of
// the Claude translation branch in sseStream: same plumbing, same buffering
// and TTFT bookkeeping, different wire format out.
func streamChatToResponses(o sseStreamOpts) error {
	return StreamChatToResponses(o.Ctx, o.W, o.Upstream, o.StartTime, o.TTFT, o.Buf)
}

// StreamChatToResponses pipes an upstream Chat Completions stream to a
// /v1/responses client as Responses events. Providers without a registered
// executor stream through the chat handler's own forwarder rather than sseStream,
// so the entry point is exported rather than reachable from one package only.
//
// The upstream may end without ever sending the usage trailer the terminal
// event is waiting on. The scan is therefore bounded once that event is owed:
// the bridge's watchdog emits response.completed, and a stalled connection is
// dropped instead of holding the client for as long as the provider likes.
func StreamChatToResponses(ctx context.Context, w http.ResponseWriter, upstream io.Reader, startTime time.Time, ttft *int64, buf io.Writer) error {
	if startTime.IsZero() {
		startTime = time.Now()
	}
	hw := proxy.NewHeartbeatWriter(ctx, w, 0)
	defer hw.Close()
	flusher := proxy.WriteSSEHeaders(hw)

	bridge := NewResponsesBridge(
		translator.RequestedModelFromContext(ctx),
		translator.CustomToolNamesFrom(ctx),
		responsesWriter(sseStreamOpts{TTFT: ttft, Buf: buf}, hw, flusher, startTime),
	)
	defer bridge.Close()

	err := proxy.ScanStreamWithDeadline(ctx, upstream, PendingCompletionFlushTimeout, bridge.CompletionPending, bridge.Feed)
	if bridge.Err() != nil {
		return fmt.Errorf("write to client: %w", bridge.Err())
	}
	return err
}

// responsesWriter wraps the client writer with the bookkeeping every translated
// stream does: stamp TTFT on the first frame, keep the response log buffer in
// step, and flush per event so the client sees them as they arrive.
func responsesWriter(o sseStreamOpts, hw *proxy.HeartbeatWriter, flusher http.Flusher, startTime time.Time) func([]byte) error {
	return func(frame []byte) error {
		if o.TTFT != nil && *o.TTFT == 0 {
			*o.TTFT = time.Since(startTime).Milliseconds()
		}
		if o.Buf != nil {
			o.Buf.Write(frame)
		}
		if _, err := hw.Write(frame); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}
}

// passthroughResponses relays an upstream Responses body untouched to a client
// that already speaks Responses. Translating it would be a round trip through
// Chat Completions that loses exactly the fields the native endpoint needs —
// previous_response_id, reasoning item ids, store — so the body is copied
// byte for byte instead.
func passthroughResponses(w http.ResponseWriter, req *Request, upstream io.Reader) error {
	// Relaying "byte for byte" means byte for byte *except* the tool names this
	// request had to have fitted for the upstream's 64-char limit. Forwarding
	// the fitted name would leave the client holding a tool_call it never
	// declared, which is the same class of break the fitting exists to avoid.
	if req.IsStream {
		return sseStream(sseStreamOpts{
			W: w, Upstream: upstream, Translate: false,
			StartTime: req.StartTime, TTFT: req.TTFT, Buf: req.ResponseBuf, Ctx: req.Ctx,
			ToolNameMap: req.ToolNameMap,
		})
	}
	body, err := io.ReadAll(io.LimitReader(upstream, constants.MaxUpstreamBodyBytes))
	if err != nil {
		return fmt.Errorf("read responses body: %w", err)
	}
	// The native endpoint relays the body byte for byte, so this is the only
	// place a blank 200 or an error envelope on a /v1/responses upstream can
	// still be caught before the client reads it as a completed turn.
	if err := proxy.EmptyUpstreamError(body); err != nil {
		return err
	}
	if len(req.ToolNameMap) > 0 {
		body = translator.RestoreToolNames(body, req.ToolNameMap)
	}
	if req.ResponseBuf != nil {
		req.ResponseBuf.Write(body)
	}
	translator.SetUsage(req.Ctx, translator.ParseResponsesUsage(body))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(body)
	return nil
}

// jsonResponseAsResponses writes a non-streaming Chat Completions body to a
// /v1/responses client in the Response shape it expects. A body the converter
// rejects is relayed unchanged rather than dropped: a body in the wrong shape
// still lets the client report the failure, an empty 200 tells it nothing.
func jsonResponseAsResponses(ctx context.Context, w http.ResponseWriter, body []byte) error {
	converted, err := translator.ChatResponseToResponses(body)
	if err == nil {
		if usage := translator.ParseResponseUsage(body); usage != nil {
			translator.SetUsage(ctx, usage)
		}
		body = converted
	} else {
		log.Error("executor", "responses json translate error", "error", err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(body)
	return nil
}
