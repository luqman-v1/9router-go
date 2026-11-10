package chat

import (
	"fmt"
	"net/http"
	"time"

	"9router/proxy/internal/constants"
	json "9router/proxy/internal/fastjson"
	"9router/proxy/internal/resilience"
)

// CheckStalledToolLoopOpenAI inspects conversation turns and writes an intervention response if stalled.
// Returns true if a stalled tool loop was detected and handled.
func (h *ChatHandler) CheckStalledToolLoopOpenAI(w http.ResponseWriter, messages []map[string]any, model string, isStream bool) bool {
	threshold := resilience.DefaultStalledToolLoopThreshold
	intervention := resilience.InspectHistoryTurns(messages, threshold)
	if intervention == nil {
		return false
	}
	writeStalledToolLoopOpenAI(w, intervention, model, isStream)
	return true
}

// CheckStalledToolLoopClaude inspects conversation turns and writes an intervention response if stalled.
// Returns true if a stalled tool loop was detected and handled.
func (h *ChatHandler) CheckStalledToolLoopClaude(w http.ResponseWriter, messages []map[string]any, model string, isStream bool) bool {
	threshold := resilience.DefaultStalledToolLoopThreshold
	intervention := resilience.InspectHistoryTurns(messages, threshold)
	if intervention == nil {
		return false
	}
	writeStalledToolLoopClaude(w, intervention, model, isStream)
	return true
}

// writeStalledToolLoopOpenAI writes a safe intervention response to an OpenAI client (streaming or non-streaming).
func writeStalledToolLoopOpenAI(w http.ResponseWriter, intervention *resilience.StalledLoopIntervention, model string, isStream bool) {
	id := fmt.Sprintf("chatcmpl-stalled-loop-%d", time.Now().Unix())
	created := time.Now().Unix()
	msg := intervention.FormatAssistantIntervention()

	if isStream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-9Router-Resilience", "stalled-tool-loop-intervention")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		// First chunk: delta content
		firstChunk := map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": []map[string]any{
				{
					"index": 0,
					"delta": map[string]any{
						"role":    "assistant",
						"content": msg,
					},
					"finish_reason": nil,
				},
			},
		}
		fc, _ := json.Marshal(firstChunk)
		_, _ = w.Write([]byte("data: "))
		_, _ = w.Write(fc)
		_, _ = w.Write([]byte("\n\n"))
		if flusher != nil {
			flusher.Flush()
		}

		// Final chunk: stop
		finalChunk := map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": []map[string]any{
				{
					"index":         0,
					"delta":         map[string]any{},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]any{
				"prompt_tokens":     1,
				"completion_tokens": 1,
				"total_tokens":      2,
			},
		}
		final, _ := json.Marshal(finalChunk)
		_, _ = w.Write([]byte("data: "))
		_, _ = w.Write(final)
		_, _ = w.Write([]byte("\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	} else {
		resp := map[string]any{
			"id":      id,
			"object":  "chat.completion",
			"created": created,
			"model":   model,
			"choices": []map[string]any{
				{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": msg,
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]any{
				"prompt_tokens":     1,
				"completion_tokens": 1,
				"total_tokens":      2,
			},
		}
		respBytes, _ := json.Marshal(resp)
		w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
		w.Header().Set("X-9Router-Resilience", "stalled-tool-loop-intervention")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(respBytes)
	}
}

// writeStalledToolLoopClaude writes a safe intervention response to a Claude /v1/messages client (streaming or non-streaming).
func writeStalledToolLoopClaude(w http.ResponseWriter, intervention *resilience.StalledLoopIntervention, model string, isStream bool) {
	id := fmt.Sprintf("msg_stalled_loop_%d", time.Now().Unix())
	msg := intervention.FormatAssistantIntervention()

	if isStream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-9Router-Resilience", "stalled-tool-loop-intervention")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		// 1. message_start
		startEvt := map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id":            id,
				"type":          "message",
				"role":          "assistant",
				"model":         model,
				"content":       []any{},
				"stop_reason":   nil,
				"stop_sequence": nil,
				"usage": map[string]any{
					"input_tokens":  1,
					"output_tokens": 1,
				},
			},
		}
		sb, _ := json.Marshal(startEvt)
		_, _ = w.Write([]byte("event: message_start\ndata: "))
		_, _ = w.Write(sb)
		_, _ = w.Write([]byte("\n\n"))

		// 2. content_block_start
		cStart := map[string]any{
			"type":  "content_block_start",
			"index": 0,
			"content_block": map[string]any{
				"type": "text",
				"text": "",
			},
		}
		csb, _ := json.Marshal(cStart)
		_, _ = w.Write([]byte("event: content_block_start\ndata: "))
		_, _ = w.Write(csb)
		_, _ = w.Write([]byte("\n\n"))

		// 3. content_block_delta
		cDelta := map[string]any{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]any{
				"type": "text_delta",
				"text": msg,
			},
		}
		cdb, _ := json.Marshal(cDelta)
		_, _ = w.Write([]byte("event: content_block_delta\ndata: "))
		_, _ = w.Write(cdb)
		_, _ = w.Write([]byte("\n\n"))

		// 4. content_block_stop
		cStop := map[string]any{
			"type":  "content_block_stop",
			"index": 0,
		}
		csStop, _ := json.Marshal(cStop)
		_, _ = w.Write([]byte("event: content_block_stop\ndata: "))
		_, _ = w.Write(csStop)
		_, _ = w.Write([]byte("\n\n"))

		// 5. message_delta
		mDelta := map[string]any{
			"type": "message_delta",
			"delta": map[string]any{
				"stop_reason":   "end_turn",
				"stop_sequence": nil,
			},
			"usage": map[string]any{
				"output_tokens": 1,
			},
		}
		mdb, _ := json.Marshal(mDelta)
		_, _ = w.Write([]byte("event: message_delta\ndata: "))
		_, _ = w.Write(mdb)
		_, _ = w.Write([]byte("\n\n"))

		// 6. message_stop
		_, _ = w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	} else {
		resp := map[string]any{
			"id":          id,
			"type":        "message",
			"role":        "assistant",
			"model":       model,
			"content":     []map[string]any{{"type": "text", "text": msg}},
			"stop_reason": "end_turn",
			"usage": map[string]any{
				"input_tokens":  1,
				"output_tokens": 1,
			},
		}
		respBytes, _ := json.Marshal(resp)
		w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
		w.Header().Set("X-9Router-Resilience", "stalled-tool-loop-intervention")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(respBytes)
	}
}
