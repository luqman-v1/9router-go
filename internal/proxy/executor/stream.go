package executor

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"9router/proxy/internal/log"
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
}

func ProcessCodexEvent(data string, state *CodexStreamState, responseID string, created int64) []string {
	var event map[string]interface{}
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
		chunk := map[string]interface{}{
			"id":      responseID,
			"object":  "chat.completion.chunk",
			"created": created,
			"choices": []map[string]interface{}{{
				"index": 0,
				"delta": map[string]interface{}{"content": delta},
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
		state.ToolCallCount++
		chunk := map[string]interface{}{
			"id":      responseID,
			"object":  "chat.completion.chunk",
			"created": created,
			"choices": []map[string]interface{}{{
				"index": 0,
				"delta": map[string]interface{}{
					"tool_calls": []map[string]interface{}{{
						"index":    state.ToolCallCount,
						"id":       fmt.Sprintf("call_%d", state.ToolCallCount),
						"type":     "function",
						"function": map[string]interface{}{"arguments": delta},
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
		chunk := map[string]interface{}{
			"id":      responseID,
			"object":  "chat.completion.chunk",
			"created": created,
			"choices": []map[string]interface{}{{
				"index": 0,
				"delta": map[string]interface{}{
					"tool_calls": []map[string]interface{}{{
						"function": map[string]interface{}{
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
		chunk := map[string]interface{}{
			"id":      responseID,
			"object":  "chat.completion.chunk",
			"created": created,
			"choices": []map[string]interface{}{{
				"index":        0,
				"delta":        map[string]interface{}{},
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

func handleCodexStream(w http.ResponseWriter, upstream io.Reader) error {
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
						continue
					}
					out := ProcessCodexEvent(data, state, responseID, created)
					for _, chunk := range out {
						w.Write([]byte(chunk))
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

	w.Write([]byte("data: [DONE]\n\n"))
	if flusher != nil {
		flusher.Flush()
	}

	translator.SetLastUsage(&translator.OpenAIUsage{
		CompletionTokens: state.OutputLength / 4,
	})

	return nil
}

// ---- Kiro EventStream → OpenAI SSE ----

type kiroStreamState struct {
	toolCallIndex int
}

func writeSSE(w io.Writer, data interface{}) {
	b, err := json.Marshal(data)
	if err != nil {
		log.Error("executor", "writeSSE marshal error", "error", err)
		return
	}
	w.Write([]byte(fmt.Sprintf("data: %s\n\n", string(b))))
}

func handleKiroStream(w http.ResponseWriter, upstream io.Reader) error {
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

			chunk := map[string]interface{}{
				"id":      responseID,
				"object":  "chat.completion.chunk",
				"created": created,
				"choices": []map[string]interface{}{{
					"index": 0,
					"delta": map[string]interface{}{"content": payload.Content},
				}},
			}
			writeSSE(w, chunk)
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
			chunk := map[string]interface{}{
				"id":      responseID,
				"object":  "chat.completion.chunk",
				"created": created,
				"choices": []map[string]interface{}{{
					"index": 0,
					"delta": map[string]interface{}{"reasoning_content": payload.Text},
				}},
			}
			writeSSE(w, chunk)
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
				chunk := map[string]interface{}{
					"id":      responseID,
					"object":  "chat.completion.chunk",
					"created": created,
					"choices": []map[string]interface{}{{
						"index": 0,
						"delta": map[string]interface{}{
							"tool_calls": []map[string]interface{}{{
								"index":    state.toolCallIndex,
								"id":       payload.ToolUseID,
								"type":     "function",
								"function": map[string]interface{}{
									"name":      payload.Name,
									"arguments": payload.Content,
								},
							}},
						},
					}},
				}
				state.toolCallIndex++
				writeSSE(w, chunk)
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
	done := map[string]interface{}{
		"id":      responseID,
		"object":  "chat.completion.chunk",
		"created": created,
		"choices": []map[string]interface{}{{
			"index":        0,
			"delta":        map[string]interface{}{},
			"finish_reason": finishReason,
		}},
	}
	writeSSE(w, done)
	w.Write([]byte("data: [DONE]\n\n"))
	if flusher != nil {
		flusher.Flush()
	}

	inputTokens := 0
	outputTokens := len(accumulatedContent.String()) / 4
	translator.SetLastUsage(&translator.OpenAIUsage{
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

func BuildCommandcodeChunk(state *CommandcodeStreamState, delta map[string]interface{}, finishReason string) string {
	chunk := map[string]interface{}{
		"id":      state.ResponseID,
		"object":  "chat.completion.chunk",
		"created": state.Created,
		"model":   state.Model,
		"choices": []map[string]interface{}{{
			"index": 0,
			"delta": delta,
		}},
	}
	if finishReason != "" {
		chunk["choices"].([]map[string]interface{})[0]["finish_reason"] = finishReason
	}
	b, err := json.Marshal(chunk)
	if err != nil {
		return ""
	}
	return string(b)
}

func handleCommandcodeStream(w http.ResponseWriter, upstream io.Reader, model string) error {
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

		var event map[string]interface{}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}
		eventType, _ := event["type"].(string)

		chunks := ProcessCommandcodeEvent(event, eventType, state)
		for _, chunk := range chunks {
			w.Write([]byte(fmt.Sprintf("data: %s\n\n", chunk)))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}

	if !state.Finished {
		finishChunk := BuildCommandcodeChunk(state, map[string]interface{}{}, "stop")
		w.Write([]byte(fmt.Sprintf("data: %s\n\n", finishChunk)))
	}
	w.Write([]byte("data: [DONE]\n\n"))
	if flusher != nil {
		flusher.Flush()
	}

	translator.SetLastUsage(&translator.OpenAIUsage{
		CompletionTokens: state.OutputLength / 4,
	})

	return scanner.Err()
}

func ProcessCommandcodeEvent(event map[string]interface{}, eventType string, state *CommandcodeStreamState) []string {
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

		delta := map[string]interface{}{"content": text}
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
		delta := map[string]interface{}{"reasoning_content": text}
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

		delta := map[string]interface{}{
			"tool_calls": []map[string]interface{}{{
				"index":    idx,
				"id":       id,
				"type":     "function",
				"function": map[string]interface{}{"name": toolName, "arguments": ""},
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
		delta := map[string]interface{}{
			"tool_calls": []map[string]interface{}{{
				"index":    idx,
				"function": map[string]interface{}{"arguments": args},
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

		delta := map[string]interface{}{
			"tool_calls": []map[string]interface{}{{
				"id":       id,
				"type":     "function",
				"function": map[string]interface{}{"name": toolName, "arguments": argsStr},
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

		chunk := BuildCommandcodeChunk(state, map[string]interface{}{}, reason)

		if totalUsage, ok := event["totalUsage"].(map[string]interface{}); ok {
			usage := map[string]interface{}{}
			if pt, ok := totalUsage["promptTokens"].(float64); ok {
				usage["prompt_tokens"] = int(pt)
			}
			if ct, ok := totalUsage["completionTokens"].(float64); ok {
				usage["completion_tokens"] = int(ct)
			}
			if len(usage) > 0 {
				var parsed map[string]interface{}
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
		delta := map[string]interface{}{"content": fmt.Sprintf("\n\n[CommandCode error: %s]", msg)}
		out = append(out, BuildCommandcodeChunk(state, delta, ""))
		out = append(out, BuildCommandcodeChunk(state, map[string]interface{}{}, "stop"))
		state.Finished = true
	}

	return out
}
