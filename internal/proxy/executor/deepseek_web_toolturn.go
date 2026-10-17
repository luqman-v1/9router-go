package executor

import (
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/proxy"
)

// deepSeekToolTurnToClient serves a request that declared tools.
//
// This path buffers the whole upstream answer before answering, which is the
// only way tool calling can work here. The model emits its tool call as text
// typed one character at a time, so a streamed answer could end at
// `{"name":"get_weather","parameters":{"` with no way to tell a truncated call
// from prose. Buffering restores the guarantee the native API gives: whatever is
// parsed is whole. The native API does not stream tool arguments either, so
// nothing is lost by not streaming here — only the text preamble a model might
// emit before the call, which is not useful once a call is returned.
//
// The isolation is deliberate. Nothing in this file is reached unless the
// request carried a `tools[]` array, and an answer that is not a declared tool
// call is passed through as ordinary content rather than being coerced into one.

// deepSeekToolTurnToClient reads the turn and writes either a tool call or the
// model's plain answer.
//
// The accumulator is the caller's: what the stream said about the answer — the
// upstream's message id, whether it signed off, whether it refused — outlives
// this read, because it decides whether the conversation can be continued.
func deepSeekToolTurnToClient(w http.ResponseWriter, req *Request, upstream io.Reader, envelope deepSeekRequestEnvelope, accumulator *deepSeekStreamAccumulator) error {
	if err := deepSeekDrain(upstream, accumulator); err != nil {
		return deepSeekFailure(http.StatusBadGateway, "deepseek-web: "+err.Error())
	}
	// A refusal the upstream streamed instead of answering reaches the client as
	// the status it is. Nothing has been written yet on this path — it buffers
	// the whole turn so a tool call's arguments arrive whole — so the failure
	// travels as a status rather than as an in-band chunk.
	if enforcement := accumulator.enforcement; enforcement != nil {
		return enforcement.error()
	}
	if !accumulator.sawFinished {
		// The same rule as the streaming path: a turn the upstream never signed
		// off is a cut-off session, not an answer. Reporting it as an empty tool
		// result would have the client retry a call that may have been made.
		return deepSeekFailure(http.StatusBadGateway,
			"deepseek-web: stream ended before completion — the upstream session was cut off")
	}

	text := accumulator.contentText()
	calls := deepSeekExtractToolCalls(text, envelope.Tools)
	if len(calls) > 0 {
		// Anything the model wrote around the calls is real content and belongs
		// to the client. Dropping it produces a message with null content, which
		// an agent reads as an empty stop and retries — the model was answering,
		// the wrapper threw the answer away.
		//
		// The text is taken once for the whole turn rather than per call: with
		// several invokes in one answer the calls are what was removed, and
		// running the helper per call would re-scan a shrinking answer for
		// brackets that are no longer there.
		raws := make([]string, 0, len(calls))
		for _, call := range calls {
			raws = append(raws, call.Raw)
		}
		preamble := deepSeekSurroundingText(text, raws...)

		if !req.IsStream {
			return deepSeekWriteToolCalls(w, req, envelope.Model, calls, preamble)
		}
		return deepSeekWriteStreamedToolCalls(w, req, envelope.Model, calls, preamble)
	}
	// A DSML block that named a tool the client never declared, or that named one
	// at all, is markup rather than an answer. Leaving it in shows the user a wall
	// of `parameter>` tags for something that was meant to be a tool call.
	if deepSeekHasDSML(text) {
		if stripped := deepSeekStripDSML(text); stripped != "" {
			text = stripped
		}
	}

	if !req.IsStream {
		return deepSeekWritePlainAnswer(w, req, envelope.Model, accumulator, text)
	}
	return deepSeekWriteStreamedAnswer(w, req, envelope.Model, accumulator, text)
}

// deepSeekAccumulate drains the upstream body into a fresh accumulator, for the
// callers that only need the text.
func deepSeekAccumulate(upstream io.Reader) (*deepSeekStreamAccumulator, error) {
	accumulator := newDeepSeekStreamAccumulator("")
	if err := deepSeekDrain(upstream, accumulator); err != nil {
		return nil, deepSeekFailure(http.StatusBadGateway, "deepseek-web: "+err.Error())
	}
	return accumulator, nil
}

// deepSeekToolPlainText returns the answer with the tool-call scaffolding
// removed when there is one, so the fallback prose path does not show the user a
// raw JSON object the extractor merely rejected for a trivial reason.
func deepSeekToolPlainText(accumulator *deepSeekStreamAccumulator) string {
	return deepSeekStripToolScaffold(accumulator.contentText())
}

// deepSeekWriteToolCall answers with an OpenAI tool_calls message, keeping any
// text the model wrote around the calls.
func deepSeekWriteToolCall(w http.ResponseWriter, req *Request, model string, call deepSeekToolRequest, preamble string) error {
	return deepSeekWriteToolCalls(w, req, model, []deepSeekToolRequest{call}, preamble)
}

// deepSeekWriteToolCalls answers with an OpenAI tool_calls message.
func deepSeekWriteToolCalls(w http.ResponseWriter, req *Request, model string, calls []deepSeekToolRequest, preamble string) error {
	return deepSeekWriteJSON(w, req, model, deepSeekToolCallMessage(calls, preamble))
}

// deepSeekWritePlainAnswer answers with the model's text when no declared tool
// was called.
func deepSeekWritePlainAnswer(w http.ResponseWriter, req *Request, model string, accumulator *deepSeekStreamAccumulator, text string) error {
	message := map[string]any{"role": "assistant", "content": text}
	if thinking := accumulator.thinkingText(); thinking != "" {
		message["reasoning_content"] = thinking
	}
	return deepSeekWriteJSON(w, req, model, message)
}

// deepSeekWriteJSON writes one chat.completion.
func deepSeekWriteJSON(w http.ResponseWriter, req *Request, model string, message map[string]any) error {
	content, _ := message["content"].(string)
	if content != "" && req.ResponseBuf != nil {
		req.ResponseBuf.Write([]byte(content))
	}

	payload := map[string]any{
		"id":      deepSeekResponseID(),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": deepSeekFinishReason(message),
		}},
		"usage": map[string]any{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("deepseek-web: marshal completion: %w", err)
	}
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(encoded); err != nil {
		return fmt.Errorf("deepseek-web: write completion: %w", err)
	}
	return nil
}

// deepSeekWriteStreamedAnswer emits the buffered answer as SSE. The turn is
// whole by the time this runs, so the text goes out as one delta rather than
// being re-split: a client reassembles deltas in order regardless, so splitting
// would only cost allocations.
func deepSeekWriteStreamedAnswer(w http.ResponseWriter, req *Request, model string, accumulator *deepSeekStreamAccumulator, text string) error {
	return deepSeekWriteStreamedMessage(w, req, model, map[string]any{
		"role":    "assistant",
		"content": text,
	}, deepSeekReasoningDelta(accumulator))
}

// deepSeekWriteStreamedToolCalls emits every recovered call as SSE. The arguments
// go out whole rather than per-character: the point of buffering was to get them
// whole, and a client reassembles them from the final frame correctly.
//
// Each call travels in its own frame, with its `index`. That field is what tells a
// client that two calls are two calls: the OpenAI stream protocol numbers every
// parallel call, and a client keys its accumulator by that number. Sending both in
// one frame left them with no index to separate them, so the client concatenated
// the two argument objects into `{}{}` — a tool call whose arguments are not
// JSON, rejected before the first command ran. The bug looked like a client fault
// and was ours.
func deepSeekWriteStreamedToolCalls(w http.ResponseWriter, req *Request, model string, calls []deepSeekToolRequest, preamble string) error {
	hw := proxy.NewHeartbeatWriter(req.Ctx, w, 0)
	defer hw.Close()
	flusher := proxy.WriteSSEHeaders(hw)

	responseID := deepSeekResponseID()
	created := time.Now().Unix()

	if start := req.StartTime; !start.IsZero() && req.TTFT != nil && *req.TTFT == 0 {
		*req.TTFT = time.Since(start).Milliseconds()
	}

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
			"id":      responseID,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": nil}},
		}
		encoded, err := json.Marshal(chunk)
		if err != nil {
			return fmt.Errorf("deepseek-web: marshal chunk: %w", err)
		}
		if _, err := hw.Write(fmt.Appendf(nil, "data: %s\n\n", encoded)); err != nil {
			return fmt.Errorf("deepseek-web: write chunk: %w", err)
		}
		if flusher != nil {
			flusher.Flush()
		}
	}

	message := deepSeekToolCallMessage(calls, preamble)
	chunk := map[string]any{
		"id":      responseID,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"delta":         map[string]any{},
			"finish_reason": deepSeekFinishReason(message),
		}},
	}
	encoded, err := json.Marshal(chunk)
	if err != nil {
		return fmt.Errorf("deepseek-web: marshal chunk: %w", err)
	}
	if _, err := hw.Write(fmt.Appendf(nil, "data: %s\n\n", encoded)); err != nil {
		return fmt.Errorf("deepseek-web: write chunk: %w", err)
	}
	if _, err := hw.Write([]byte("data: [DONE]\n\n")); err != nil {
		return fmt.Errorf("deepseek-web: write terminator: %w", err)
	}
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}

// deepSeekWriteStreamedMessage writes one buffered message in the SSE shape a
// streaming client expects, and closes the stream properly.
//
// This path existed and always wrote a single chat.completion document, whatever
// the client asked for. A client that sent stream:true received an object with no
// `data:` frames at all and waited forever for a terminator that was never going
// // to arrive — which is what a hanging request is, from the client's side.
func deepSeekWriteStreamedMessage(w http.ResponseWriter, req *Request, model string, message map[string]any, reasoning map[string]any) error {
	hw := proxy.NewHeartbeatWriter(req.Ctx, w, 0)
	defer hw.Close()
	flusher := proxy.WriteSSEHeaders(hw)

	responseID := deepSeekResponseID()
	created := time.Now().Unix()

	if start := req.StartTime; !start.IsZero() && req.TTFT != nil && *req.TTFT == 0 {
		*req.TTFT = time.Since(start).Milliseconds()
	}

	emit := func(delta map[string]any, finish any) error {
		chunk := map[string]any{
			"id":      responseID,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}},
		}
		encoded, err := json.Marshal(chunk)
		if err != nil {
			return fmt.Errorf("deepseek-web: marshal chunk: %w", err)
		}
		if _, err := hw.Write(fmt.Appendf(nil, "data: %s\n\n", encoded)); err != nil {
			return fmt.Errorf("deepseek-web: write chunk: %w", err)
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}

	delta := map[string]any{}
	for key, value := range reasoning {
		delta[key] = value
	}
	if content, _ := message["content"].(string); content != "" && req.ResponseBuf != nil {
		req.ResponseBuf.Write([]byte(content))
	}
	if calls, ok := message["tool_calls"]; ok {
		delta["tool_calls"] = calls
		delta["role"] = "assistant"
	}
	if content, _ := message["content"].(string); content != "" {
		delta["content"] = content
	}
	if _, ok := delta["role"]; !ok {
		delta["role"] = "assistant"
	}
	if err := emit(delta, nil); err != nil {
		return err
	}
	if err := emit(map[string]any{}, deepSeekFinishReason(message)); err != nil {
		return err
	}
	_, err := hw.Write([]byte("data: [DONE]\n\n"))
	if flusher != nil {
		flusher.Flush()
	}
	return err
}

// deepSeekToolCallMessage builds the tool_calls message, keeping any text the
// model wrote around the calls.
func deepSeekToolCallMessage(calls []deepSeekToolRequest, preamble string) map[string]any {
	list := make([]any, 0, len(calls))
	for _, call := range calls {
		list = append(list, map[string]any{
			"id":   deepSeekToolCallID(call.Name),
			"type": "function",
			"function": map[string]any{
				"name":      call.Name,
				"arguments": call.Arguments,
			},
		})
	}
	message := map[string]any{
		"role":       "assistant",
		"content":    nil,
		"tool_calls": list,
	}
	if preamble != "" {
		message["content"] = preamble
	}
	return message
}

// deepSeekReasoningDelta returns the reasoning channel when the model used one.
func deepSeekReasoningDelta(accumulator *deepSeekStreamAccumulator) map[string]any {
	thinking := accumulator.thinkingText()
	if thinking == "" {
		return nil
	}
	return map[string]any{"reasoning_content": thinking}
}

// deepSeekFinishReason reports "tool_calls" only when a call is present, which
// is what tells a client to run the tool rather than print the text.
func deepSeekFinishReason(message map[string]any) string {
	if calls, ok := message["tool_calls"]; ok {
		if list, ok := calls.([]any); ok && len(list) > 0 {
			return "tool_calls"
		}
	}
	return "stop"
}

// deepSeekStripToolScaffold removes a leading instruction echo.
//
// The prompt tells the model to reply with a single JSON object, and models
// sometimes echo that instruction back before complying. When the answer is not
// a declared tool call, the echoed scaffolding is noise the user would read, so
// it is dropped — but only when what follows still has content, so an answer
// that is *only* the echo is never emptied.
func deepSeekStripToolScaffold(text string) string {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") {
		return trimmed
	}
	end := strings.LastIndex(trimmed, "}")
	if end > 0 && end < len(trimmed)-1 {
		tail := strings.TrimSpace(trimmed[end+1:])
		if tail != "" && !strings.HasPrefix(tail, "{") {
			return trimmed[:end+1] + "\n" + tail
		}
		return trimmed[:end+1]
	}
	return trimmed
}
