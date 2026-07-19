package handlers

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/translator"
)

// forwardCommandcodeRequest handles CommandCode AI SDK v5 NDJSON → OpenAI SSE translation.
func (h *ChatHandler) forwardCommandcodeRequest(
	w http.ResponseWriter,
	cfg *providers.ProviderConfig,
	apiKey string,
	body []byte,
	isStream bool,
	translateResponse bool,
	metrics *streamMetrics,
) error {
	var oreq struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &oreq)

	// Force stream=true — commandcode always uses NDJSON
	var reqMap map[string]interface{}
	if err := json.Unmarshal(body, &reqMap); err != nil {
		return fmt.Errorf("parse body: %w", err)
	}
	reqMap["stream"] = true
	reqBody, _ := json.Marshal(reqMap)

	req, err := http.NewRequest("POST", cfg.BaseURL, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("x-session-id", uuid.New().String())
	req.Header.Set("x-command-code-version", "0.25.7")
	req.Header.Set("x-cli-environment", "cli")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := h.Client.Do(req)
	if err != nil {
		return fmt.Errorf("upstream request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return &upstreamError{StatusCode: resp.StatusCode, Body: errBody}
	}

	return h.handleCommandcodeStream(w, resp.Body, oreq.Model)
}

func (h *ChatHandler) handleCommandcodeStream(w http.ResponseWriter, upstream io.Reader, model string) error {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	state := &commandcodeStreamState{
		responseID: fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		created:    time.Now().Unix(),
		model:      model,
	}

	scanner := bufio.NewScanner(upstream)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		// Try SSE-style "data:" prefix or raw NDJSON
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

		chunks := processCommandcodeEvent(event, eventType, state)
		for _, chunk := range chunks {
			w.Write([]byte(fmt.Sprintf("data: %s\n\n", chunk)))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}

	// Emit finish if stream ended without it
	if !state.finished {
		finishChunk := buildCommandcodeChunk(state, map[string]interface{}{}, "stop")
		w.Write([]byte(fmt.Sprintf("data: %s\n\n", finishChunk)))
	}
	w.Write([]byte("data: [DONE]\n\n"))
	if flusher != nil {
		flusher.Flush()
	}

	// Set estimated usage
	translator.SetLastUsage(&translator.OpenAIUsage{
		CompletionTokens: state.outputLength / 4,
	})

	return scanner.Err()
}

type commandcodeStreamState struct {
	responseID    string
	created       int64
	model         string
	chunkIndex    int
	toolIndex     int
	toolIndexByID map[string]int
	outputLength  int
	finishReason  string
	finished      bool
}

func buildCommandcodeChunk(state *commandcodeStreamState, delta map[string]interface{}, finishReason string) string {
	chunk := map[string]interface{}{
		"id":      state.responseID,
		"object":  "chat.completion.chunk",
		"created": state.created,
		"model":   state.model,
		"choices": []map[string]interface{}{{
			"index": 0,
			"delta": delta,
		}},
	}
	if finishReason != "" {
		chunk["choices"].([]map[string]interface{})[0]["finish_reason"] = finishReason
	}
	b, _ := json.Marshal(chunk)
	return string(b)
}

func processCommandcodeEvent(event map[string]interface{}, eventType string, state *commandcodeStreamState) []string {
	if state.toolIndexByID == nil {
		state.toolIndexByID = make(map[string]int)
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
		state.outputLength += len(text)

		delta := map[string]interface{}{"content": text}
		if state.chunkIndex == 0 {
			delta["role"] = "assistant"
		}
		state.chunkIndex++
		out = append(out, buildCommandcodeChunk(state, delta, ""))

	case "reasoning-delta":
		text, _ := event["text"].(string)
		if text == "" {
			return nil
		}
		delta := map[string]interface{}{"reasoning_content": text}
		if state.chunkIndex == 0 {
			delta["role"] = "assistant"
		}
		state.chunkIndex++
		out = append(out, buildCommandcodeChunk(state, delta, ""))

	case "tool-input-start":
		id, _ := event["id"].(string)
		if id == "" {
			if v, ok := event["toolCallId"].(string); ok {
				id = v
			} else {
				id = fmt.Sprintf("call_%d", state.toolIndex)
			}
		}
		if _, exists := state.toolIndexByID[id]; !exists {
			state.toolIndexByID[id] = state.toolIndex
			state.toolIndex++
		}
		idx := state.toolIndexByID[id]
		toolName, _ := event["toolName"].(string)

		delta := map[string]interface{}{
			"tool_calls": []map[string]interface{}{{
				"index":    idx,
				"id":       id,
				"type":     "function",
				"function": map[string]interface{}{"name": toolName, "arguments": ""},
			}},
		}
		out = append(out, buildCommandcodeChunk(state, delta, ""))

	case "tool-input-delta":
		id, _ := event["id"].(string)
		if id == "" {
			if v, ok := event["toolCallId"].(string); ok {
				id = v
			}
		}
		idx, exists := state.toolIndexByID[id]
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
		out = append(out, buildCommandcodeChunk(state, delta, ""))

	case "tool-call":
		id, _ := event["toolCallId"].(string)
		if id == "" {
			return nil
		}
		if _, exists := state.toolIndexByID[id]; exists {
			return nil // already handled via tool-input-*
		}
		idx := state.toolIndex
		state.toolIndexByID[id] = idx
		state.toolIndex++

		toolName, _ := event["toolName"].(string)
		var argsStr string
		if input, ok := event["input"].(string); ok {
			argsStr = input
		} else if input, ok := event["input"]; ok {
			b, _ := json.Marshal(input)
			argsStr = string(b)
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
		if state.chunkIndex == 0 {
			delta["role"] = "assistant"
		}
		state.chunkIndex++
		out = append(out, buildCommandcodeChunk(state, delta, ""))

	case "finish-step":
		if reason, ok := event["finishReason"].(string); ok {
			state.finishReason = reason
		}

	case "finish":
		reason := state.finishReason
		if reason == "" {
			if r, ok := event["finishReason"].(string); ok {
				reason = r
			}
			if reason == "" {
				reason = "stop"
			}
		}
		state.finished = true

		chunk := buildCommandcodeChunk(state, map[string]interface{}{}, reason)

		// Add usage if present
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
				json.Unmarshal([]byte(chunk), &parsed)
				parsed["usage"] = usage
				b, _ := json.Marshal(parsed)
				chunk = string(b)
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
		out = append(out, buildCommandcodeChunk(state, delta, ""))
		out = append(out, buildCommandcodeChunk(state, map[string]interface{}{}, "stop"))
		state.finished = true
	}

	return out
}
