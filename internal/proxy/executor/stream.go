package executor

import (
	"bufio"
	"bytes"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
	"9router/proxy/internal/translator"
)

// ---- Codex Responses API SSE → Chat SSE ----

type CodexStreamState struct {
	CurrentEvent      string
	OutputLength      int
	ToolCallCount     int
	Completed         bool   // response.completed seen — finish chunk already emitted
	UpstreamErr       []byte // raw upstream {"type":"error",...} event, if any
	CurrentToolCallID string
	ToolCallIdx       map[string]int
	ToolCallNames     map[string]string
	ToolCallArgs      map[string]string
	ItemIDToIdx       map[string]int
	ArgsEmitted       map[int]bool
}

func ProcessCodexEvent(data string, state *CodexStreamState, responseID string, created int64) []string {
	var event map[string]any
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return nil
	}

	eventType, _ := event["type"].(string)

	// Upstream error events (e.g. {"type":"error","error":{...FreeTierError}})
	// must surface, never vanish: without this the converter below emits a
	// 200 with empty content for a failed turn (silent success). Record on
	// state; handleCodexStream converts to an UpstreamError after the scan.
	if eventType == "error" {
		if raw, err := json.Marshal(event); err == nil {
			state.UpstreamErr = raw
		} else {
			state.UpstreamErr = []byte(`{"type":"error"}`)
		}
		return nil
	}

	switch eventType {
	case "response.output_text.delta", "response.text.delta":
		delta, _ := event["delta"].(string)
		if delta == "" {
			return nil
		}
		state.OutputLength += len(delta)
		chunk := map[string]any{
			"id":      responseID,
			"object":  "chat.completion.chunk",
			"created": created,
			"choices": []map[string]any{{
				"index": 0,
				"delta": map[string]any{"content": delta},
			}},
		}
		b, err := json.Marshal(chunk)
		if err != nil {
			return nil
		}
		return []string{fmt.Sprintf("data: %s\n\n", string(b))}

	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta", "response.thought.delta":
		delta, _ := event["delta"].(string)
		if delta == "" {
			return nil
		}
		state.OutputLength += len(delta)
		chunk := map[string]any{
			"id":      responseID,
			"object":  "chat.completion.chunk",
			"created": created,
			"choices": []map[string]any{{
				"index": 0,
				"delta": map[string]any{"reasoning_content": delta},
			}},
		}
		b, err := json.Marshal(chunk)
		if err != nil {
			return nil
		}
		return []string{fmt.Sprintf("data: %s\n\n", string(b))}
	case "response.output_item.added":
		item, _ := event["item"].(map[string]any)
		if item == nil {
			return nil
		}
		itemType, _ := item["type"].(string)
		if itemType == "function_call" || itemType == "custom_tool_call" {
			name, _ := item["name"].(string)
			callID, _ := item["call_id"].(string)
			if callID == "" {
				callID, _ = item["id"].(string)
			}
			if callID == "" {
				callID = fmt.Sprintf("call_%d", state.ToolCallCount)
			}
			itemID, _ := event["item_id"].(string)
			if itemID == "" {
				itemID, _ = item["id"].(string)
			}
			if state.ToolCallIdx == nil {
				state.ToolCallIdx = make(map[string]int)
			}
			if state.ItemIDToIdx == nil {
				state.ItemIDToIdx = make(map[string]int)
			}
			if state.ToolCallNames == nil {
				state.ToolCallNames = make(map[string]string)
			}
			idx, ok := state.ToolCallIdx[callID]
			if !ok && itemID != "" {
				idx, ok = state.ItemIDToIdx[itemID]
			}
			if !ok {
				idx = state.ToolCallCount
				state.ToolCallIdx[callID] = idx
				state.ToolCallCount++
			}
			if itemID != "" {
				state.ItemIDToIdx[itemID] = idx
			}
			state.ToolCallNames[callID] = name
			state.CurrentToolCallID = callID

			chunk := map[string]any{
				"id":      responseID,
				"object":  "chat.completion.chunk",
				"created": created,
				"choices": []map[string]any{{
					"index": 0,
					"delta": map[string]any{
						"tool_calls": []map[string]any{{
							"index": idx,
							"id":    callID,
							"type":  "function",
							"function": map[string]any{
								"name":      name,
								"arguments": "",
							},
						}},
					},
				}},
			}
			b, err := json.Marshal(chunk)
			if err != nil {
				return nil
			}
			return []string{fmt.Sprintf("data: %s\n\n", string(b))}
		}

	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		delta, _ := event["delta"].(string)
		if delta == "" {
			return nil
		}
		itemID, _ := event["item_id"].(string)
		callID, _ := event["call_id"].(string)
		name, _ := event["name"].(string)
		var idx int
		var ok bool
		if itemID != "" && state.ItemIDToIdx != nil {
			idx, ok = state.ItemIDToIdx[itemID]
		}
		if !ok && callID != "" && state.ToolCallIdx != nil {
			idx, ok = state.ToolCallIdx[callID]
		}
		if !ok {
			if callID == "" {
				callID = state.CurrentToolCallID
			}
			if callID == "" {
				callID = fmt.Sprintf("call_%d", state.ToolCallCount)
			}
			if state.ToolCallIdx == nil {
				state.ToolCallIdx = make(map[string]int)
			}
			idx, ok = state.ToolCallIdx[callID]
			if !ok {
				idx = state.ToolCallCount
				state.ToolCallIdx[callID] = idx
				state.ToolCallCount++
			}
		}

		if state.ToolCallArgs == nil {
			state.ToolCallArgs = make(map[string]string)
		}
		state.ToolCallArgs[callID] += delta
		if state.ArgsEmitted == nil {
			state.ArgsEmitted = make(map[int]bool)
		}
		state.ArgsEmitted[idx] = true
		fnMap := map[string]any{
			"arguments": delta,
		}
		// Only include name if not already sent (avoid triplication across added/delta/done)
		if name != "" {
			if existing, ok := state.ToolCallNames[callID]; !ok || existing == "" {
				fnMap["name"] = name
			} else if existing != name {
				// For split names, only append remainder
				if !strings.Contains(existing, name) {
					fnMap["name"] = name
				}
			}
		}
		tcMap := map[string]any{
			"index":    idx,
			"id":       callID,
			"type":     "function",
			"function": fnMap,
		}

		chunk := map[string]any{
			"id":      responseID,
			"object":  "chat.completion.chunk",
			"created": created,
			"choices": []map[string]any{{
				"index": 0,
				"delta": map[string]any{
					"tool_calls": []map[string]any{tcMap},
				},
			}},
		}
		b, err := json.Marshal(chunk)
		if err != nil {
			return nil
		}
		return []string{fmt.Sprintf("data: %s\n\n", string(b))}

	case "response.function_call_arguments.done":
		name, _ := event["name"].(string)
		args, _ := event["arguments"].(string)
		itemID, _ := event["item_id"].(string)
		callID, _ := event["call_id"].(string)
		var idx int
		var ok bool
		if itemID != "" && state.ItemIDToIdx != nil {
			idx, ok = state.ItemIDToIdx[itemID]
		}
		if !ok && callID != "" && state.ToolCallIdx != nil {
			idx, ok = state.ToolCallIdx[callID]
		}
		if !ok {
			if callID == "" {
				callID = state.CurrentToolCallID
			}
			if callID == "" {
				callID = fmt.Sprintf("call_%d", state.ToolCallCount)
			}
			if state.ToolCallIdx == nil {
				state.ToolCallIdx = make(map[string]int)
			}
			idx, ok = state.ToolCallIdx[callID]
			if !ok {
				idx = state.ToolCallCount
				state.ToolCallIdx[callID] = idx
				state.ToolCallCount++
			}
		}
		// If name wasn't captured before, use it now
		if name == "" && state.ToolCallNames != nil {
			name = state.ToolCallNames[callID]
		}
		if name != "" && state.ToolCallNames != nil {
			if _, ok := state.ToolCallNames[callID]; !ok || state.ToolCallNames[callID] == "" {
				state.ToolCallNames[callID] = name
			}
		}
		// Avoid duplicating arguments already sent via delta
		if state.ToolCallArgs == nil {
			state.ToolCallArgs = make(map[string]string)
		}
		if existing, ok := state.ToolCallArgs[callID]; ok && existing != "" {
			if existing == args || strings.Contains(existing, args) {
				// Already sent via delta, skip emitting done
				return nil
			}
			if strings.HasPrefix(args, existing) {
				// Only send the remaining suffix
				args = strings.TrimPrefix(args, existing)
				if args == "" {
					return nil
				}
			}
		}
		if args != "" {
			state.ToolCallArgs[callID] += args
		}
		// Only include name if not already sent
		fnMapDone := map[string]any{
			"arguments": args,
		}
		if name != "" {
			if _, ok := state.ToolCallNames[callID]; !ok {
				fnMapDone["name"] = name
			} else if state.ToolCallNames[callID] != name && !strings.Contains(state.ToolCallNames[callID], name) {
				fnMapDone["name"] = name
			}
		}
		// If both name and args would be empty, skip
		if len(fnMapDone) == 1 && fnMapDone["arguments"] == "" {
			return nil
		}
		chunk := map[string]any{
			"id":      responseID,
			"object":  "chat.completion.chunk",
			"created": created,
			"choices": []map[string]any{{
				"index": 0,
				"delta": map[string]any{
					"tool_calls": []map[string]any{{
						"index":    idx,
						"id":       callID,
						"type":     "function",
						"function": fnMapDone,
					}},
				},
			}},
		}
		b, err := json.Marshal(chunk)
		if err != nil {
			return nil
		}
		return []string{fmt.Sprintf("data: %s\n\n", string(b))}

	case "response.output_item.done":
		item, _ := event["item"].(map[string]any)
		if item != nil {
			itemType, _ := item["type"].(string)
			if itemType == "function_call" || itemType == "custom_tool_call" {
				state.CurrentToolCallID = ""
				itemID, _ := event["item_id"].(string)
				if itemID == "" {
					itemID, _ = item["id"].(string)
				}
				callID, _ := item["call_id"].(string)
				if callID == "" {
					callID = itemID
				}
				var idx int
				var ok bool
				if itemID != "" && state.ItemIDToIdx != nil {
					idx, ok = state.ItemIDToIdx[itemID]
				}
				if !ok && callID != "" && state.ToolCallIdx != nil {
					idx, ok = state.ToolCallIdx[callID]
				}
				fullArgs, _ := item["arguments"].(string)
				if fullArgs != "" {
					if state.ArgsEmitted != nil && state.ArgsEmitted[idx] {
						return nil
					}
					if existing, ok := state.ToolCallArgs[callID]; ok && existing != "" {
						return nil
					}
					if state.ToolCallArgs == nil {
						state.ToolCallArgs = make(map[string]string)
					}
					state.ToolCallArgs[callID] = fullArgs
					if state.ArgsEmitted == nil {
						state.ArgsEmitted = make(map[int]bool)
					}
					state.ArgsEmitted[idx] = true
					chunk := map[string]any{
						"id":      responseID,
						"object":  "chat.completion.chunk",
						"created": created,
						"choices": []map[string]any{{
							"index": 0,
							"delta": map[string]any{
								"tool_calls": []map[string]any{{
									"index": idx,
									"function": map[string]any{
										"arguments": fullArgs,
									},
								}},
							},
						}},
					}
					b, err := json.Marshal(chunk)
					if err == nil {
						return []string{fmt.Sprintf("data: %s\n\n", string(b))}
					}
				}
			}
		}
		return nil
	case "response.completed":
		state.Completed = true
		finishReason := "stop"
		if state.ToolCallCount > 0 {
			finishReason = "tool_calls"
		}
		chunk := map[string]any{
			"id":      responseID,
			"object":  "chat.completion.chunk",
			"created": created,
			"choices": []map[string]any{{
				"index":         0,
				"delta":         map[string]any{},
				"finish_reason": finishReason,
			}},
		}
		if resp, ok := event["response"].(map[string]any); ok {
			if chunkUsage := codexUsageMap(resp); len(chunkUsage) > 0 {
				chunk["usage"] = chunkUsage
			}
		}
		b, err := json.Marshal(chunk)
		if err != nil {
			return nil
		}
		return []string{fmt.Sprintf("data: %s\n\n", string(b))}
	}

	return nil
}

// codexUsageMap converts the Responses-shaped usage carried by a
// response.completed event into the Chat Completions usage block that the
// downstream translators and usage accounting read. Cache reads and reasoning
// tokens are carried over verbatim: both are billed at their own rate, so
// losing either under-counts the turn.
func codexUsageMap(resp map[string]any) map[string]any {
	usage, ok := resp["usage"].(map[string]any)
	if !ok {
		return nil
	}

	chunkUsage := map[string]any{}
	for _, f := range []struct{ from, to string }{
		{"input_tokens", "prompt_tokens"},
		{"output_tokens", "completion_tokens"},
		{"total_tokens", "total_tokens"},
	} {
		if v, ok := usage[f.from].(float64); ok {
			chunkUsage[f.to] = int(v)
		}
	}
	if inDetails, ok := usage["input_tokens_details"].(map[string]any); ok {
		if cTok, ok := inDetails["cached_tokens"].(float64); ok {
			chunkUsage["prompt_tokens_details"] = map[string]any{"cached_tokens": int(cTok)}
		}
	}
	if outDetails, ok := usage["output_tokens_details"].(map[string]any); ok {
		if rTok, ok := outDetails["reasoning_tokens"].(float64); ok {
			chunkUsage["completion_tokens_details"] = map[string]any{"reasoning_tokens": int(rTok)}
		}
	}
	return chunkUsage
}

// toolCallIndex returns the OpenAI tool-call id + index for a Codex event.
// It uses the upstream call_id (falling back to a stable fabricated id) and
// assigns an index on first sight, so all fragments of one call share them.
func toolCallIndex(state *CodexStreamState, event map[string]any) (string, int) {
	if state.ToolCallIdx == nil {
		state.ToolCallIdx = make(map[string]int)
	}
	id, _ := event["call_id"].(string)
	if id == "" {
		id = state.CurrentToolCallID
	}
	if id == "" {
		id = fmt.Sprintf("call_%d", state.ToolCallCount)
	}
	if idx, ok := state.ToolCallIdx[id]; ok {
		return id, idx
	}
	idx := state.ToolCallCount
	state.ToolCallIdx[id] = idx
	state.ToolCallCount++
	return id, idx
}

func handleCodexStream(w http.ResponseWriter, req *Request, upstream io.Reader) error {
	responseID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	created := time.Now().Unix()
	state := &CodexStreamState{}

	// A Responses client and a Responses endpoint already agree: relaying the
	// body untouched keeps the fields the native API owns (previous_response_id,
	// item ids, store) instead of round-tripping them through Chat Completions.
	if translator.IsResponsesClient(req.Ctx) {
		return passthroughResponses(w, req, upstream)
	}

	if req.IsStream {
		hw := proxy.NewHeartbeatWriter(req.Ctx, w, 0)
		defer hw.Close()

		if !req.TranslateResp {
			// Streaming to OpenAI-compatible client
			flusher := proxy.WriteSSEHeaders(hw)
			doneSeen := false

			err := proxy.ScanStream(upstream, func(payload []byte) {
				if doneSeen {
					return
				}
				data := string(payload)
				if data == "[DONE]" {
					doneSeen = true
					_ = writeSSEFinish(hw, flusher, req, state, responseID, created)
					return
				}

				out := ProcessCodexEvent(data, state, responseID, created)
				for _, chunk := range out {
					if req.TTFT != nil && *req.TTFT == 0 {
						*req.TTFT = time.Since(req.StartTime).Milliseconds()
					}
					if req.ResponseBuf != nil {
						req.ResponseBuf.Write([]byte(chunk))
					}
					if _, werr := hw.Write([]byte(chunk)); werr != nil {
						return
					}
				}
				if flusher != nil {
					flusher.Flush()
				}
			})

			// Headers are already committed on the stream path, so a pure
			// upstream error (no content at all) can only be signaled by
			// closing without useful chunks; the non-stream path below
			// returns a proper UpstreamError instead.
			if !doneSeen {
				return writeSSEFinish(hw, flusher, req, state, responseID, created)
			}
			return err
		}

		// Streaming with Claude translation (/v1/messages)
		flusher := proxy.WriteSSEHeaders(hw)
		sessionKey := fmt.Sprintf("stream-%d", time.Now().UnixNano())
		defer translator.ClearStreamState(sessionKey)

		doneSeen := false
		err := proxy.ScanStream(upstream, func(payload []byte) {
			if doneSeen {
				return
			}
			data := string(payload)
			if data == "[DONE]" {
				doneSeen = true
				return
			}

			out := ProcessCodexEvent(data, state, responseID, created)
			for _, chunk := range out {
				chunkBytes := []byte(chunk)
				translated, terr := translator.TranslateOpenAIToClaudeStreamSession(sessionKey, chunkBytes)
				if terr != nil {
					log.Error("executor", "translate codex stream error", "error", terr)
					continue
				}
				if translated == nil {
					continue
				}
				if req.TTFT != nil && *req.TTFT == 0 {
					*req.TTFT = time.Since(req.StartTime).Milliseconds()
				}
				if req.ResponseBuf != nil {
					req.ResponseBuf.Write(translated)
				}
				if _, werr := hw.Write(translated); werr != nil {
					return
				}
				if flusher != nil {
					flusher.Flush()
				}
			}
		})

		// Finish stream
		if !state.Completed {
			finishChunk := []byte(fmt.Sprintf("data: %s\n\n", fmt.Sprintf(`{"id":"%s","object":"chat.completion.chunk","created":%d,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, responseID, created)))
			if translated, terr := translator.TranslateOpenAIToClaudeStreamSession(sessionKey, finishChunk); terr == nil && translated != nil {
				hw.Write(translated)
			}
		}
		doneChunk := []byte("data: [DONE]\n\n")
		if translated, terr := translator.TranslateOpenAIToClaudeStreamSession(sessionKey, doneChunk); terr == nil && translated != nil {
			hw.Write(translated)
		}
		if flusher != nil {
			flusher.Flush()
		}

		if usage := translator.GetStreamUsage(sessionKey); usage != nil {
			translator.SetUsage(req.Ctx, usage)
		} else {
			translator.SetUsage(req.Ctx, &translator.OpenAIUsage{
				CompletionTokens: state.OutputLength / 4,
			})
		}
		return err
	}

	// Non-streaming (req.IsStream == false). sseBuf is capped: a slow,
	// huge, or endless upstream stream must not expand the heap without
	// bound (same 10MB discipline as proxy.openai non-stream reads).
	const maxCodexSSEBytes = 10 << 20
	var sseBuf bytes.Buffer
	var sseTruncated bool
	_ = proxy.ScanStream(upstream, func(payload []byte) {
		if sseTruncated {
			return
		}
		data := string(payload)
		if data == "[DONE]" {
			return
		}
		out := ProcessCodexEvent(data, state, responseID, created)
		for _, chunk := range out {
			if sseBuf.Len()+len(chunk) > maxCodexSSEBytes {
				sseTruncated = true
				return
			}
			sseBuf.WriteString(chunk)
		}
	})

	// Upstream error events must fail loudly, never masquerade as an empty
	// 200. Parse status/message from the recorded event (default 502).
	if len(state.UpstreamErr) > 0 && state.OutputLength == 0 && state.ToolCallCount == 0 {
		return codexUpstreamError(state.UpstreamErr)
	}
	converted, ok := sseToOpenAIJSON(sseBuf.Bytes())
	if !ok {
		if len(state.UpstreamErr) > 0 {
			return codexUpstreamError(state.UpstreamErr)
		}
		// Upstream answered 200 with an event stream that never carried a
		// completion. Fabricating `content: ""` here read to the client as a
		// served empty turn and ended combo fallback on this model, so report
		// the 502 and let the router move on.
		return proxy.UpstreamFailure(http.StatusBadGateway, proxy.NoCompletionInStream)
	}
	return jsonResponse(req.Ctx, w, bytes.NewReader(converted), req.TranslateResp, req.ResponseBuf)
}

// codexUpstreamError converts a recorded upstream {"type":"error",...} event
// into an UpstreamError (default 502). FreeTierError / access gates map to
// 403 so fallback treats them as credential problems, not capacity.
func codexUpstreamError(raw []byte) error {
	var event struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	status := http.StatusBadGateway
	msg := "upstream error"
	if err := json.Unmarshal(raw, &event); err == nil {
		if event.Error.Message != "" {
			msg = event.Error.Message
		}
		t := strings.ToLower(event.Error.Type)
		if strings.Contains(t, "freetier") || strings.Contains(t, "forbidden") || strings.Contains(t, "auth") {
			status = http.StatusForbidden
		}
	}
	body, _ := json.Marshal(map[string]any{
		"error": map[string]any{"message": msg, "type": "upstream_error", "code": status},
	})
	return &proxy.UpstreamError{StatusCode: status, Body: body}
}

// writeSSEFinish writes the closing [DONE] frame and records usage. If the
// upstream never sent response.completed, it emits the finish chunk first so
// clients always see a terminal frame exactly once.
func writeSSEFinish(w http.ResponseWriter, flusher http.Flusher, req *Request, state *CodexStreamState, responseID string, created int64) error {
	if !state.Completed {
		chunk := map[string]any{
			"id":      responseID,
			"object":  "chat.completion.chunk",
			"created": created,
			"choices": []map[string]any{{
				"index":         0,
				"delta":         map[string]any{},
				"finish_reason": "stop",
			}},
		}
		if b, err := json.Marshal(chunk); err == nil {
			if _, werr := w.Write([]byte(fmt.Sprintf("data: %s\n\n", string(b)))); werr != nil {
				return werr
			}
		}
	}
	if _, err := w.Write([]byte("data: [DONE]\n\n")); err != nil {
		return err
	}
	if flusher != nil {
		flusher.Flush()
	}

	translator.SetUsage(req.Ctx, &translator.OpenAIUsage{
		CompletionTokens: state.OutputLength / 4,
	})
	return nil
}

// ---- Kiro EventStream → OpenAI SSE ----

// kiroToolCall accumulates one toolUseEvent. Kiro streams the arguments as
// several frames — each carrying a slice of the JSON text under `input` — and
// closes the tool with a `stop: true` frame, so arguments are only complete at
// the end of the stream.
type kiroToolCall struct {
	id      string
	name    string
	inputs  strings.Builder
	emitted bool
}

type kiroStreamState struct {
	toolCallIndex int
	tools         []*kiroToolCall
}

// tool returns the buffer for id, creating it on first sight.
func (s *kiroStreamState) tool(id, name string) *kiroToolCall {
	for _, existing := range s.tools {
		if existing.id == id {
			return existing
		}
	}
	call := &kiroToolCall{id: id, name: name}
	s.tools = append(s.tools, call)
	return call
}

func writeSSE(w io.Writer, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("writeSSE marshal: %w", err)
	}
	if _, err := w.Write([]byte(fmt.Sprintf("data: %s\n\n", string(b)))); err != nil {
		return err
	}
	return nil
}

func handleKiroStream(w http.ResponseWriter, req *Request, upstream io.Reader) error {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	esr := providers.NewEventStreamReader(upstream)

	responseID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	created := time.Now().Unix()
	state := &kiroStreamState{}

	var accumulatedContent strings.Builder
	var readErr error

	for {
		frame, err := esr.ReadFrame()
		if err != nil {
			log.Error("executor", "kiro eventstream error", "error", err)
			readErr = err
			break
		}
		if frame == nil {
			break
		}

		eventType := frame.Headers[":event-type"]

		switch eventType {
		case "assistantResponseEvent":
			var payload struct {
				Content string `json:"content"`
			}
			if err := json.Unmarshal(frame.Payload, &payload); err != nil {
				continue
			}
			if payload.Content == "" {
				continue
			}
			accumulatedContent.WriteString(payload.Content)

			chunk := map[string]any{
				"id":      responseID,
				"object":  "chat.completion.chunk",
				"created": created,
				"choices": []map[string]any{{
					"index": 0,
					"delta": map[string]any{"content": payload.Content},
				}},
			}
			if err := writeSSE(w, chunk); err != nil {
				return err
			}
			if flusher != nil {
				flusher.Flush()
			}

		case "reasoningContentEvent":
			var payload struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(frame.Payload, &payload); err != nil {
				continue
			}
			if payload.Text == "" {
				continue
			}
			chunk := map[string]any{
				"id":      responseID,
				"object":  "chat.completion.chunk",
				"created": created,
				"choices": []map[string]any{{
					"index": 0,
					"delta": map[string]any{"reasoning_content": payload.Text},
				}},
			}
			if err := writeSSE(w, chunk); err != nil {
				return err
			}
			if flusher != nil {
				flusher.Flush()
			}

		case "toolUseEvent":
			// Kiro fragments one tool call across frames: the first carries only
			// name+toolUseId, the middle frames carry slices of the JSON
			// arguments under `input`, and a final `stop: true` frame closes it.
			// Emitting per frame is what produced `arguments: "{}"`.
			var raw any
			if err := json.Unmarshal(frame.Payload, &raw); err != nil {
				continue
			}
			values := []any{raw}
			if list, ok := raw.([]any); ok {
				values = list
			}
			for _, item := range values {
				value, ok := item.(map[string]any)
				if !ok {
					continue
				}
				name, _ := value["name"].(string)
				name = strings.TrimSpace(name)
				if name == "" {
					continue
				}
				toolUseID, _ := value["toolUseId"].(string)
				if strings.TrimSpace(toolUseID) == "" {
					toolUseID = fmt.Sprintf("call_%d_%d", created, len(state.tools)+1)
				}
				call := state.tool(toolUseID, name)
				switch input := value["input"].(type) {
				case string:
					call.inputs.WriteString(input)
				case map[string]any:
					if encoded, err := json.Marshal(input); err == nil {
						call.inputs.Write(encoded)
					}
				}
			}
		}
	}
	// Tool calls are only complete once the stream ends, so emit them here with
	// the fully reassembled arguments.
	for _, call := range state.tools {
		if call.emitted {
			continue
		}
		arguments := strings.TrimSpace(call.inputs.String())
		if arguments == "" {
			arguments = "{}"
		}
		chunk := map[string]any{
			"id":      responseID,
			"object":  "chat.completion.chunk",
			"created": created,
			"choices": []map[string]any{{
				"index": 0,
				"delta": map[string]any{
					"tool_calls": []map[string]any{{
						"index": state.toolCallIndex,
						"id":    call.id,
						"type":  "function",
						"function": map[string]any{
							"name":      call.name,
							"arguments": arguments,
						},
					}},
				},
			}},
		}
		call.emitted = true
		state.toolCallIndex++
		if err := writeSSE(w, chunk); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
	}

	finishReason := "stop"
	if state.toolCallIndex > 0 {
		finishReason = "tool_calls"
	}
	done := map[string]any{
		"id":      responseID,
		"object":  "chat.completion.chunk",
		"created": created,
		"choices": []map[string]any{{
			"index":         0,
			"delta":         map[string]any{},
			"finish_reason": finishReason,
		}},
	}
	if err := writeSSE(w, done); err != nil {
		return err
	}
	if _, err := w.Write([]byte("data: [DONE]\n\n")); err != nil {
		return err
	}
	if flusher != nil {
		flusher.Flush()
	}

	inputTokens := 0
	outputTokens := len(accumulatedContent.String()) / 4
	translator.SetUsage(req.Ctx, &translator.OpenAIUsage{
		PromptTokens:     inputTokens,
		CompletionTokens: outputTokens,
	})

	return readErr
}

// sseCollectWriter is an http.ResponseWriter that keeps only the body. It lets
// a handler that must emit SSE internally be reused for a non-streaming
// request, whose frames are folded afterwards. The header and status are
// discarded: the non-streaming path owns the real response headers.
type sseCollectWriter struct {
	buf bytes.Buffer
}

func (w *sseCollectWriter) Header() http.Header         { return http.Header{} }
func (w *sseCollectWriter) WriteHeader(int)             {}
func (w *sseCollectWriter) Write(b []byte) (int, error) { return w.buf.Write(b) }

// handleKiroNonStream answers a `stream:false` request. Kiro's gateway speaks
// only EventStream, so the frames are produced exactly as they are for a
// streaming client and then folded into one chat.completion. Without this the
// client received `Content-Type: text/event-stream` and a body its
// JSON.parse could not read (issue #41).
func handleKiroNonStream(w http.ResponseWriter, req *Request, upstream io.Reader) error {
	collect := &sseCollectWriter{}
	// A clean end of stream surfaces as io.EOF once the last frame is
	// consumed, which is the normal case here and not a failure; anything
	// else is a real read error and must not be answered as a completion.
	if err := handleKiroStream(collect, req, upstream); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	folded, ok := sseToOpenAIJSON(collect.buf.Bytes())
	if !ok {
		return proxy.UpstreamFailure(http.StatusBadGateway, proxy.NoCompletionInStream)
	}
	return jsonResponse(req.Ctx, w, bytes.NewReader(folded), req.TranslateResp, req.ResponseBuf)
}

// ---- CommandCode NDJSON → OpenAI SSE ----

type CommandcodeStreamState struct {
	ResponseID    string
	Created       int64
	Model         string
	ChunkIndex    int
	ToolIndex     int
	ToolIndexByID map[string]int
	OutputLength  int
	FinishReason  string
	Finished      bool
}

func BuildCommandcodeChunk(state *CommandcodeStreamState, delta map[string]any, finishReason string) string {
	chunk := map[string]any{
		"id":      state.ResponseID,
		"object":  "chat.completion.chunk",
		"created": state.Created,
		"model":   state.Model,
		"choices": []map[string]any{{
			"index": 0,
			"delta": delta,
		}},
	}
	if finishReason != "" {
		chunk["choices"].([]map[string]any)[0]["finish_reason"] = finishReason
	}
	b, err := json.Marshal(chunk)
	if err != nil {
		return ""
	}
	return string(b)
}

// ParseCommandCodeError extracts HTTP status code and message from a CommandCode error event.
func ParseCommandCodeError(event map[string]any) (int, string) {
	if event == nil {
		return 503, "CommandCode upstream error"
	}
	message := "CommandCode upstream error"
	statusCode := 503

	if errVal, ok := event["error"]; ok {
		if errMap, ok := errVal.(map[string]any); ok {
			if msg, ok := errMap["message"].(string); ok && msg != "" {
				message = msg
			}
			if sc, ok := errMap["statusCode"].(float64); ok && sc >= 400 && sc <= 599 {
				statusCode = int(sc)
			} else if sc, ok := errMap["status"].(float64); ok && sc >= 400 && sc <= 599 {
				statusCode = int(sc)
			}
		} else if msg, ok := errVal.(string); ok && msg != "" {
			message = msg
		}
	} else if msg, ok := event["message"].(string); ok && msg != "" {
		message = msg
	}

	if sc, ok := event["statusCode"].(float64); ok && sc >= 400 && sc <= 599 {
		statusCode = int(sc)
	}

	if statusCode == 503 {
		lower := strings.ToLower(message)
		if strings.Contains(lower, "rate limit") || strings.Contains(lower, "too many requests") {
			statusCode = 429
		} else if strings.Contains(lower, "unauthorized") || strings.Contains(lower, "invalid api key") || strings.Contains(lower, "authentication") {
			statusCode = 401
		} else if strings.Contains(lower, "payment required") || strings.Contains(lower, "billing") {
			statusCode = 402
		} else if strings.Contains(lower, "quota") || strings.Contains(lower, "forbidden") || strings.Contains(lower, "permission") {
			statusCode = 403
		} else if strings.Contains(lower, "not found") {
			statusCode = 404
		}
	}

	return statusCode, message
}

func handleCommandcodeStream(w http.ResponseWriter, req *Request, upstream io.Reader, model string) error {
	state := &CommandcodeStreamState{
		ResponseID: fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		Created:    time.Now().Unix(),
		Model:      model,
	}

	scanner := bufio.NewScanner(upstream)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	var headersWritten bool

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		data := line
		if strings.HasPrefix(line, "data:") {
			data = strings.TrimSpace(line[5:])
		}
		if data == "[DONE]" {
			continue
		}

		var event map[string]any
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}
		eventType, _ := event["type"].(string)

		if eventType == "error" && !headersWritten {
			code, msg := ParseCommandCodeError(event)
			return &proxy.UpstreamError{
				StatusCode: code,
				Body:       []byte(fmt.Sprintf(`{"error":{"message":"%s","code":%d}}`, msg, code)),
			}
		}

		chunks := ProcessCommandcodeEvent(event, eventType, state)
		if len(chunks) == 0 {
			continue
		}

		if !headersWritten {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.WriteHeader(http.StatusOK)
			headersWritten = true
		}

		flusher, _ := w.(http.Flusher)
		for _, chunk := range chunks {
			if _, err := w.Write([]byte(fmt.Sprintf("data: %s\n\n", chunk))); err != nil {
				return err
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}

	if !headersWritten {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		headersWritten = true
	}

	flusher, _ := w.(http.Flusher)
	if !state.Finished {
		finishChunk := BuildCommandcodeChunk(state, map[string]any{}, "stop")
		if _, err := w.Write([]byte(fmt.Sprintf("data: %s\n\n", finishChunk))); err != nil {
			return err
		}
	}
	if _, err := w.Write([]byte("data: [DONE]\n\n")); err != nil {
		return err
	}
	if flusher != nil {
		flusher.Flush()
	}

	translator.SetUsage(req.Ctx, &translator.OpenAIUsage{
		CompletionTokens: state.OutputLength / 4,
	})

	return scanner.Err()
}

func ProcessCommandcodeEvent(event map[string]any, eventType string, state *CommandcodeStreamState) []string {
	if state.ToolIndexByID == nil {
		state.ToolIndexByID = make(map[string]int)
	}

	var out []string

	switch eventType {
	case "text-delta":
		text, _ := event["text"].(string)
		if text == "" {
			if d, ok := event["delta"].(string); ok {
				text = d
			}
		}
		if text == "" {
			return nil
		}
		state.OutputLength += len(text)

		delta := map[string]any{"content": text}
		if state.ChunkIndex == 0 {
			delta["role"] = "assistant"
		}
		state.ChunkIndex++
		out = append(out, BuildCommandcodeChunk(state, delta, ""))

	case "reasoning-delta":
		text, _ := event["text"].(string)
		if text == "" {
			return nil
		}
		delta := map[string]any{"reasoning_content": text}
		if state.ChunkIndex == 0 {
			delta["role"] = "assistant"
		}
		state.ChunkIndex++
		out = append(out, BuildCommandcodeChunk(state, delta, ""))

	case "tool-input-start":
		id, _ := event["id"].(string)
		if id == "" {
			if v, ok := event["toolCallId"].(string); ok {
				id = v
			} else {
				id = fmt.Sprintf("call_%d", state.ToolIndex)
			}
		}
		if _, exists := state.ToolIndexByID[id]; !exists {
			state.ToolIndexByID[id] = state.ToolIndex
			state.ToolIndex++
		}
		idx := state.ToolIndexByID[id]
		toolName, _ := event["toolName"].(string)

		delta := map[string]any{
			"tool_calls": []map[string]any{{
				"index":    idx,
				"id":       id,
				"type":     "function",
				"function": map[string]any{"name": toolName, "arguments": ""},
			}},
		}
		out = append(out, BuildCommandcodeChunk(state, delta, ""))

	case "tool-input-delta":
		id, _ := event["id"].(string)
		if id == "" {
			if v, ok := event["toolCallId"].(string); ok {
				id = v
			}
		}
		idx, exists := state.ToolIndexByID[id]
		if !exists {
			return nil
		}
		args, _ := event["delta"].(string)
		delta := map[string]any{
			"tool_calls": []map[string]any{{
				"index":    idx,
				"id":       id,
				"function": map[string]any{"arguments": args},
			}},
		}
		out = append(out, BuildCommandcodeChunk(state, delta, ""))

	case "tool-call":
		id, _ := event["toolCallId"].(string)
		if id == "" {
			return nil
		}
		if _, exists := state.ToolIndexByID[id]; exists {
			return nil
		}
		idx := state.ToolIndex
		state.ToolIndexByID[id] = idx
		state.ToolIndex++

		toolName, _ := event["toolName"].(string)
		var argsStr string
		if input, ok := event["input"].(string); ok {
			argsStr = input
		} else if input, ok := event["input"]; ok {
			b, marshalErr := json.Marshal(input)
			if marshalErr != nil {
				argsStr = "{}"
			} else {
				argsStr = string(b)
			}
		}
		if argsStr == "" {
			argsStr = "{}"
		}

		delta := map[string]any{
			"tool_calls": []map[string]any{{
				"id":       id,
				"type":     "function",
				"function": map[string]any{"name": toolName, "arguments": argsStr},
			}},
		}
		if state.ChunkIndex == 0 {
			delta["role"] = "assistant"
		}
		state.ChunkIndex++
		out = append(out, BuildCommandcodeChunk(state, delta, ""))

	case "finish-step":
		if reason, ok := event["finishReason"].(string); ok {
			state.FinishReason = reason
		}

	case "finish":
		reason := state.FinishReason
		if reason == "" {
			if r, ok := event["finishReason"].(string); ok {
				reason = r
			}
			if reason == "" {
				reason = "stop"
			}
		}
		state.Finished = true

		chunk := BuildCommandcodeChunk(state, map[string]any{}, reason)

		if totalUsage, ok := event["totalUsage"].(map[string]any); ok {
			usage := map[string]any{}
			if pt, ok := totalUsage["promptTokens"].(float64); ok {
				usage["prompt_tokens"] = int(pt)
			}
			if ct, ok := totalUsage["completionTokens"].(float64); ok {
				usage["completion_tokens"] = int(ct)
			}
			if len(usage) > 0 {
				var parsed map[string]any
				if err := json.Unmarshal([]byte(chunk), &parsed); err != nil {
					log.Warn("executor", "commandcode unmarshal chunk", "error", err)
				} else {
					parsed["usage"] = usage
					b, marshalErr := json.Marshal(parsed)
					if marshalErr != nil {
						log.Warn("executor", "commandcode marshal chunk", "error", marshalErr)
					} else {
						chunk = string(b)
					}
				}
			}
		}
		out = append(out, chunk)

	case "error":
		msg, _ := event["error"].(string)
		if msg == "" {
			if m, ok := event["message"].(string); ok {
				msg = m
			}
		}
		if msg == "" {
			msg = "unknown error"
		}
		delta := map[string]any{"content": fmt.Sprintf("\n\n[CommandCode error: %s]", msg)}
		out = append(out, BuildCommandcodeChunk(state, delta, ""))
		out = append(out, BuildCommandcodeChunk(state, map[string]any{}, "stop"))
		state.Finished = true
	}

	return out
}
