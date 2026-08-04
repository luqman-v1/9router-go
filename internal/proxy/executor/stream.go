package executor

import (
	"9router/proxy/internal/log"
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/translator"
)

// ---- Codex Responses API SSE → Chat SSE ----

type CodexStreamState struct {
	CurrentEvent  string
	OutputLength  int
	ToolCallCount int
	Completed     bool // response.completed seen — finish chunk already emitted
	// ToolCallIdx maps the upstream call_id → the OpenAI tool-call index
	// assigned when the call first appears, so all argument deltas for the
	// same tool share one index/id (not a fresh one per fragment).
	ToolCallIdx map[string]int
}

func ProcessCodexEvent(data string, state *CodexStreamState, responseID string, created int64) []string {
	var event map[string]any
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return nil
	}

	eventType, _ := event["type"].(string)

	switch eventType {
	case "response.output_text.delta":
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

	case "response.function_call_arguments.delta":
		delta, _ := event["delta"].(string)
		if delta == "" {
			return nil
		}
		id, idx := toolCallIndex(state, event)
		chunk := map[string]any{
			"id":      responseID,
			"object":  "chat.completion.chunk",
			"created": created,
			"choices": []map[string]any{{
				"index": 0,
				"delta": map[string]any{
					"tool_calls": []map[string]any{{
						"index":    idx,
						"id":       id,
						"type":     "function",
						"function": map[string]any{"arguments": delta},
					}},
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
		if name == "" {
			return nil
		}
		// Reuse the same index/id that streamed the incremental arguments.
		id, idx := toolCallIndex(state, event)
		chunk := map[string]any{
			"id":      responseID,
			"object":  "chat.completion.chunk",
			"created": created,
			"choices": []map[string]any{{
				"index": 0,
				"delta": map[string]any{
					"tool_calls": []map[string]any{{
						"index": idx,
						"id":    id,
						"type":  "function",
						"function": map[string]any{
							"name":      name,
							"arguments": args,
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

	case "response.completed":
		state.Completed = true
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
		b, err := json.Marshal(chunk)
		if err != nil {
			return nil
		}
		return []string{fmt.Sprintf("data: %s\n\n", string(b))}
	}

	return nil
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
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	responseID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	created := time.Now().Unix()
	state := &CodexStreamState{}

	buf := make([]byte, 64*1024)
	var leftover string

	for {
		n, err := upstream.Read(buf)
		if n > 0 {
			text := leftover + string(buf[:n])
			leftover = ""

			for _, line := range strings.Split(text, "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}

				if strings.HasPrefix(line, "event: ") {
					state.CurrentEvent = line[7:]
					continue
				}

				if strings.HasPrefix(line, "data: ") {
					data := line[6:]
					if data == "[DONE]" {
						return writeSSEFinish(w, flusher, req, state, responseID, created)
					}
					out := ProcessCodexEvent(data, state, responseID, created)
					for _, chunk := range out {
						if _, werr := w.Write([]byte(chunk)); werr != nil {
							return werr
						}
					}
					if flusher != nil {
						flusher.Flush()
					}
				}
			}
		}
		if err != nil {
			break
		}
	}

	return writeSSEFinish(w, flusher, req, state, responseID, created)
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

type kiroStreamState struct {
	toolCallIndex int
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
			var payload struct {
				ToolUseID string `json:"toolUseId"`
				Content   string `json:"content"`
				Name      string `json:"name"`
			}
			if err := json.Unmarshal(frame.Payload, &payload); err != nil {
				continue
			}

			if payload.Content != "" {
				chunk := map[string]any{
					"id":      responseID,
					"object":  "chat.completion.chunk",
					"created": created,
					"choices": []map[string]any{{
						"index": 0,
						"delta": map[string]any{
							"tool_calls": []map[string]any{{
								"index": state.toolCallIndex,
								"id":    payload.ToolUseID,
								"type":  "function",
								"function": map[string]any{
									"name":      payload.Name,
									"arguments": payload.Content,
								},
							}},
						},
					}},
				}
				state.toolCallIndex++
				if err := writeSSE(w, chunk); err != nil {
					return err
				}
				if flusher != nil {
					flusher.Flush()
				}
			}
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

func handleCommandcodeStream(w http.ResponseWriter, req *Request, upstream io.Reader, model string) error {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	state := &CommandcodeStreamState{
		ResponseID: fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		Created:    time.Now().Unix(),
		Model:      model,
	}

	scanner := bufio.NewScanner(upstream)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

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

		chunks := ProcessCommandcodeEvent(event, eventType, state)
		for _, chunk := range chunks {
			if _, err := w.Write([]byte(fmt.Sprintf("data: %s\n\n", chunk))); err != nil {
				return err
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}

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
