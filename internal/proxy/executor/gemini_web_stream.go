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
}

func newGeminiWebEmitter(w http.ResponseWriter, req *Request, model string) *geminiWebEmitter {
	hw := proxy.NewHeartbeatWriter(req.Ctx, w, 0)
	return &geminiWebEmitter{
		req:     req,
		hw:      hw,
		id:      "chatcmpl-gemini-web-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		created: time.Now().Unix(),
		model:   model,
	}
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
	return e.emit(map[string]any{"content": delta}, nil)
}

// emitThinkingDelta sends the part of the reasoning the client has not seen.
func (e *geminiWebEmitter) emitThinkingDelta(thoughts string) error {
	delta, sent, ok, err := geminiWebDelta(e.sentThink, thoughts)
	if err != nil || !ok {
		return err
	}
	e.sentThink = sent
	return e.emit(map[string]any{"reasoning_content": delta}, nil)
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

// emit writes one chunk, announcing the role on the first frame.
func (e *geminiWebEmitter) emit(delta map[string]any, finish any) error {
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
	encoded, err := json.Marshal(chunk)
	if err != nil {
		return fmt.Errorf("gemini-web: marshal chunk: %w", err)
	}
	e.open()
	if _, err := e.hw.Write(fmt.Appendf(nil, "data: %s\n\n", encoded)); err != nil {
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
func (e *geminiWebEmitter) finish() error {
	if !e.opened {
		// An answer that was never emitted still has to be a well-formed stream.
		if err := e.emit(map[string]any{"content": ""}, nil); err != nil {
			return err
		}
	}
	if err := e.emit(nil, "stop"); err != nil {
		return err
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
	if _, err := e.hw.Write(proxy.BuildStreamErrorBytes(status, message, proxy.SSEFormatOpenAI)); err != nil {
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
	return geminiWebWriteJSON(w, req, model, message, "stop")
}

// geminiWebWriteToolCalls writes a tool-calling turn.
//
// Tool calling is buffered on this provider — the site has no native tool
// channel, so the calls are recovered from the answer's text once it is whole,
// which is the same approach deepseek-web uses and the same parser does the
// recovery.
func geminiWebWriteToolCalls(w http.ResponseWriter, req *Request, model string, calls []deepSeekToolRequest, preamble string) error {
	message := deepSeekToolCallMessage(calls, preamble)
	if req.IsStream {
		return geminiWebWriteStreamedToolCalls(w, req, model, calls, preamble)
	}
	return geminiWebWriteJSON(w, req, model, message, deepSeekFinishReason(message))
}

// geminiWebWriteStreamedToolCalls emits recovered calls as SSE. Each call travels
// in its own frame with its `index`, which is what tells a client that two calls
// are two calls; the arguments go out whole, because buffering them was the point.
func geminiWebWriteStreamedToolCalls(w http.ResponseWriter, req *Request, model string, calls []deepSeekToolRequest, preamble string) error {
	hw := proxy.NewHeartbeatWriter(req.Ctx, w, 0)
	defer hw.Close()
	flusher := proxy.WriteSSEHeaders(hw)
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	created := time.Now().Unix()

	for i, call := range calls {
		delta := map[string]any{
			"role": "assistant",
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
		chunk := map[string]any{
			"id":      "chatcmpl-gemini-web-" + suffix,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": nil}},
		}
		encoded, err := json.Marshal(chunk)
		if err != nil {
			return fmt.Errorf("gemini-web: marshal chunk: %w", err)
		}
		if _, err := hw.Write(fmt.Appendf(nil, "data: %s\n\n", encoded)); err != nil {
			return fmt.Errorf("gemini-web: write chunk: %w", err)
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
	if err := geminiWebWriteTerminalChunk(hw, flusher, model, suffix, created, deepSeekFinishReason(deepSeekToolCallMessage(calls, preamble))); err != nil {
		return err
	}
	if _, err := hw.Write([]byte("data: [DONE]\n\n")); err != nil {
		return fmt.Errorf("gemini-web: write terminator: %w", err)
	}
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}

// geminiWebWriteTerminalChunk closes a stream with the finish reason.
func geminiWebWriteTerminalChunk(hw *proxy.HeartbeatWriter, flusher http.Flusher, model, suffix string, created int64, finish string) error {
	chunk := map[string]any{
		"id":      "chatcmpl-gemini-web-" + suffix,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": finish}},
	}
	encoded, err := json.Marshal(chunk)
	if err != nil {
		return fmt.Errorf("gemini-web: marshal chunk: %w", err)
	}
	if _, err := hw.Write(fmt.Appendf(nil, "data: %s\n\n", encoded)); err != nil {
		return fmt.Errorf("gemini-web: write chunk: %w", err)
	}
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}

// geminiWebWriteJSON writes one chat.completion through the shared response path.
func geminiWebWriteJSON(w http.ResponseWriter, req *Request, model string, message map[string]any, finish string) error {
	body := map[string]any{
		"id":      "chatcmpl-gemini-web-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{"index": 0, "message": message, "finish_reason": finish}},
		"usage":   map[string]any{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("gemini-web: marshal completion: %w", err)
	}
	return jsonResponse(req.Ctx, w, bytes.NewReader(encoded), req.TranslateResp, req.ResponseBuf)
}
