package executor

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/proxy"
	"9router/proxy/internal/translator"
)

// geminiWebEmitter writes a streaming answer.
//
// It owns three things a turn cannot express on its own: the delta arithmetic
// (the site sends the answer *so far*, the client expects what is new), the
// refusal to revise an answer already sent, and the heartbeat that keeps a
// client's idle timeout from firing while the site is still thinking.
type geminiWebEmitter struct {
	req       *Request
	hw        *proxy.HeartbeatWriter
	flusher   http.Flusher
	id        string
	created   int64
	model     string
	sentText  int
	sentThink int
	roleSent  bool
	opened    bool

	// sessionKey scopes the Claude translation's state to this turn. The
	// translator keeps it per key, so two turns in flight do not share a
	// block counter.
	sessionKey string
	// bridge is the Responses client path: the OpenAI chunks are replayed
	// as the Responses events a /v1/responses client waits for.
	bridge *ResponsesBridge
	// claude marks a /v1/messages client. The chunks this emitter writes
	// are OpenAI chunks, and a Claude client cannot read them, so they are
	// translated on the way out.
	claude bool
}

// newGeminiWebEmitter builds the writer for one turn.
//
// The shape of the answer depends on the client: a Chat Completions client
// gets the OpenAI chunks this emitter builds, a /v1/messages client gets the
// Claude events the translator produces from them, and a /v1/responses client
// gets the Responses events the bridge produces from them. A client that
// receives the wrong shape sees a turn that looks like an upstream answering
// in the wrong format.
func newGeminiWebEmitter(w http.ResponseWriter, req *Request, model string) *geminiWebEmitter {
	hw := proxy.NewHeartbeatWriter(req.Ctx, w, 0)
	emitter := &geminiWebEmitter{
		req:        req,
		hw:         hw,
		id:         "chatcmpl-gemini-web-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		created:    time.Now().Unix(),
		model:      model,
		sessionKey: fmt.Sprintf("gemini-web-stream-%d", time.Now().UnixNano()),
		claude:     req.TranslateResp,
	}
	// A /v1/responses client is one hop further out than a Chat one: the
	// OpenAI chunks this emitter produces become Responses events, so the
	// bridge consumes them rather than the client.
	if translator.NeedsResponsesBridge(req.Ctx) {
		emitter.bridge = NewResponsesBridge(
			translator.RequestedModelFromContext(req.Ctx),
			translator.CustomToolNamesFrom(req.Ctx),
			emitter.writeResponsesFrame,
		)
	}
	return emitter
}

// open writes the SSE headers on the first frame the client will see.
//
// The status is not committed until there is something to say, so a turn that
// fails before its first frame — an unknown model, a refused session — still has
// the status the router needs to act on instead of a 200 that carries nothing.
func (e *geminiWebEmitter) open() {
	if e.opened {
		return
	}
	e.opened = true
	e.flusher = proxy.WriteSSEHeaders(e.hw)
}

// emitContentDelta sends the part of the answer the client has not seen, and
// mirrors it into the response buffer.
//
// The buffer is what the usage recorder and the request-details log read the
// answer out of. A streaming path that skips it produces a request whose logged
// body is empty and whose token estimate is zero, which looks like the model
// answered nothing — so every streaming executor in this package writes it
// (forward.go:140, gemini_handler.go:531, claude_messages.go:39).
func (e *geminiWebEmitter) emitContentDelta(text string) error {
	delta, sent, ok, err := geminiWebDelta(e.sentText, text)
	if err != nil || !ok {
		return err
	}
	e.sentText = sent
	if e.req.ResponseBuf != nil {
		if _, err := e.req.ResponseBuf.Write([]byte(delta)); err != nil {
			return err
		}
	}
	return e.writeChunk(map[string]any{"content": delta}, nil, nil)
}

// emitThinkingDelta sends the part of the reasoning the client has not seen.
func (e *geminiWebEmitter) emitThinkingDelta(thoughts string) error {
	delta, sent, ok, err := geminiWebDelta(e.sentThink, thoughts)
	if err != nil || !ok {
		return err
	}
	e.sentThink = sent
	return e.writeChunk(map[string]any{"reasoning_content": delta}, nil, nil)
}

// geminiWebDelta computes what is new between what the client was already sent
// and what the site now reports for the whole answer.
//
// Everything the site sends is the whole answer, so a value that is not an
// extension of the previous one is the site revising an answer the client has
// already rendered. That is refused: the alternative is a client whose first
// half and second half disagree, with nothing to tell it which to keep.
func geminiWebDelta(sent int, current string) (delta string, next int, changed bool, err error) {
	if len(current) < sent {
		return "", sent, false, errors.New(
			"gemini-web: gemini.google.com revised an answer it had already streamed")
	}
	if len(current) == sent {
		return "", sent, false, nil
	}
	return current[sent:], len(current), true, nil
}

// writeChunk writes one OpenAI chunk in the shape the client asked for.
//
// A Chat Completions client gets the chunk as it is. A /v1/messages client
// cannot read an OpenAI chunk, so the chunk is translated into the Claude
// events it expects. A /v1/responses client is one hop further out, and the
// bridge replays the chunk as the Responses events that client waits for.
//
// Without this routing a Claude client received OpenAI frames it cannot
// parse, and the turn looked like an upstream answering in the wrong format.
func (e *geminiWebEmitter) writeChunk(delta map[string]any, finish any, usage map[string]any) error {
	if delta == nil {
		delta = map[string]any{}
	}
	if !e.roleSent {
		e.roleSent = true
		delta["role"] = "assistant"
	}
	e.markTTFT()
	chunk := map[string]any{
		"id":      e.id,
		"object":  "chat.completion.chunk",
		"created": e.created,
		"model":   e.model,
		"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}},
	}
	if usage != nil {
		chunk["usage"] = usage
	}
	encoded, err := json.Marshal(chunk)
	if err != nil {
		return fmt.Errorf("gemini-web: marshal chunk: %w", err)
	}
	if e.bridge != nil {
		e.bridge.Feed(encoded)
		return e.bridge.Err()
	}
	frame := fmt.Appendf(nil, "data: %s\n\n", encoded)
	if e.claude {
		translated, terr := translator.TranslateOpenAIToClaudeStreamSession(e.sessionKey, encoded)
		if terr != nil {
			return terr
		}
		if translated != nil {
			frame = translated
		}
	}
	e.open()
	if _, err := e.hw.Write(frame); err != nil {
		return err
	}
	if e.flusher != nil {
		e.flusher.Flush()
	}
	return nil
}

// writeResponsesFrame writes one Responses event to the client.
func (e *geminiWebEmitter) writeResponsesFrame(frame []byte) error {
	e.open()
	if e.req.ResponseBuf != nil {
		if _, err := e.req.ResponseBuf.Write(frame); err != nil {
			return err
		}
	}
	if _, err := e.hw.Write(frame); err != nil {
		return err
	}
	if e.flusher != nil {
		e.flusher.Flush()
	}
	return nil
}

// markTTFT records the first byte the client will see, once.
func (e *geminiWebEmitter) markTTFT() {
	if e.req.TTFT == nil || *e.req.TTFT != 0 {
		return
	}
	start := e.req.StartTime
	if start.IsZero() {
		start = time.Now()
	}
	*e.req.TTFT = time.Since(start).Milliseconds()
}

// finish closes a stream that was written as it went.
//
// The usage frame rides the last chunk, which is where OpenAI's own servers
// put it: a client that asked for stream_options.include_usage reads the
// counts from the final frame, and a client that did not simply ignores a
// field it does not know.
func (e *geminiWebEmitter) finish(usage map[string]any) error {
	return e.finishWithReason("stop", usage)
}

// finishWithReason closes the stream with a specific finish reason. A turn
// that ended in a tool call finishes with "tool_calls", which the Claude
// translation turns into the "tool_use" stop reason.
func (e *geminiWebEmitter) finishWithReason(reason string, usage map[string]any) error {
	if !e.opened {
		// An answer that was never emitted still has to be a well-formed stream.
		if err := e.writeChunk(map[string]any{"content": ""}, nil, nil); err != nil {
			return err
		}
	}
	if err := e.writeChunk(nil, reason, usage); err != nil {
		return err
	}
	if e.bridge != nil {
		// A Responses client waits for response.completed, not [DONE], so the
		// bridge closes the stream itself.
		e.bridge.StopCompletionWatchdog()
		e.bridge.Close()
		return e.hw.Close()
	}
	if e.claude {
		// The Claude stream ends with message_delta and message_stop, which
		// the translator emits with the finish reason. A stream that never
		// carried one still has to be closed, or the client waits forever.
		if end := translator.EnsureStreamClosed(e.sessionKey); len(end) > 0 {
			if _, err := e.hw.Write(end); err != nil {
				return err
			}
		}
		translator.ClearStreamState(e.sessionKey)
		return e.hw.Close()
	}
	if _, err := e.hw.Write([]byte("data: [DONE]\n\n")); err != nil {
		return err
	}
	if e.flusher != nil {
		e.flusher.Flush()
	}
	return e.hw.Close()
}

// abort reports an upstream failure after the stream has opened. HTTP 200 is on
// the wire by then, so the failure travels as a chunk carrying an error; a client
// left waiting for a terminator that never comes is the alternative.
func (e *geminiWebEmitter) abort(cause error) error {
	if !e.opened {
		// Nothing has been written, so the failure can still be a status.
		_ = e.hw.Close()
		return cause
	}
	status, message := proxy.ClassifyStreamAbort(cause)
	format := proxy.SSEFormatOpenAI
	if e.claude {
		format = proxy.SSEFormatClaude
	}
	if _, err := e.hw.Write(proxy.BuildStreamErrorBytes(status, message, format)); err != nil {
		return cause
	}
	if e.flusher != nil {
		e.flusher.Flush()
	}
	_ = e.hw.Close()
	return cause
}

// geminiWebWriteCompletion writes a whole answer as a chat.completion.
//
// The document goes through the shared response path, so a /v1/responses or
// Claude client receives the shape it asked for, outbound guardrails still
// apply, and the log buffer records the same text every other provider does.
func geminiWebWriteCompletion(w http.ResponseWriter, req *Request, model string, turn *geminiWebTurn) error {
	message := map[string]any{"role": "assistant", "content": turn.text}
	if thinking := strings.TrimSpace(turn.thinking); thinking != "" {
		message["reasoning_content"] = thinking
	}
	return geminiWebWriteJSON(w, req, model, message, "stop", turn.usage())
}

// geminiWebWriteToolCalls writes a tool-calling turn.
//
// Tool calling is buffered on this provider — the site has no native tool
// channel, so the calls are recovered from the answer's text once it is whole,
// which is the same approach deepseek-web uses and the same parser does the
// recovery.
func geminiWebWriteToolCalls(w http.ResponseWriter, req *Request, model string, calls []deepSeekToolRequest, preamble string, usage map[string]any) error {
	message := deepSeekToolCallMessage(calls, preamble)
	if req.IsStream {
		return geminiWebWriteStreamedToolCalls(w, req, model, calls, preamble, usage)
	}
	return geminiWebWriteJSON(w, req, model, message, deepSeekFinishReason(message), usage)
}

// geminiWebWriteStreamedToolCalls emits recovered calls as SSE. Each call travels
// in its own frame with its `index`, which is what tells a client that two calls
// are two calls; the arguments go out whole, because buffering them was the point.
//
// The frames go through the emitter, so a Claude or Responses client gets the
// shape it asked for instead of the OpenAI chunks this provider produces.
func geminiWebWriteStreamedToolCalls(w http.ResponseWriter, req *Request, model string, calls []deepSeekToolRequest, preamble string, usage map[string]any) error {
	emitter := newGeminiWebEmitter(w, req, model)
	for i, call := range calls {
		delta := map[string]any{
			"tool_calls": []any{map[string]any{
				"index": i,
				"id":    deepSeekToolCallID(call.Name),
				"type":  "function",
				"function": map[string]any{
					"name":      call.Name,
					"arguments": call.Arguments,
				},
			}},
		}
		// The model's prose belongs to the first frame: a client records content
		// as it arrives, and only the assistant turn starts with it.
		if i == 0 && preamble != "" {
			delta["content"] = preamble
			if req.ResponseBuf != nil {
				req.ResponseBuf.Write([]byte(preamble))
			}
		}
		if err := emitter.writeChunk(delta, nil, nil); err != nil {
			return err
		}
	}
	return emitter.finishWithReason("tool_calls", usage)
}

// geminiWebWriteJSON writes one chat.completion through the shared response path.
func geminiWebWriteJSON(w http.ResponseWriter, req *Request, model string, message map[string]any, finish string, usage map[string]any) error {
	if usage == nil {
		usage = map[string]any{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0}
	}
	body := map[string]any{
		"id":      "chatcmpl-gemini-web-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{"index": 0, "message": message, "finish_reason": finish}},
		"usage":   usage,
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("gemini-web: marshal completion: %w", err)
	}
	return jsonResponse(req.Ctx, w, bytes.NewReader(encoded), req.TranslateResp, req.ResponseBuf)
}
