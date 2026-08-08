package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/proxy"
)

// ForwardCodebuddyCN forwards to Tencent CodeBuddy with force-stream
// and reasoning_summary injection.
//
// CodeBuddy is OpenAI-compatible but rejects non-stream chat requests
// (HTTP 400, code 11101 "Non-stream chat request is currently not supported").
// This executor forces stream=true in the request body, mirroring the
// JS version's CodeBuddyExecutor.transformRequest behaviour.
//
// It also handles reasoning_effort:
//   - "none"/"off" → stripped (CodeBuddy gateway has no "none")
//   - any other value → injects reasoning_summary="auto" so CodeBuddy
//     surfaces the model's reasoning output.
func ForwardCodebuddyCN(w http.ResponseWriter, req *Request) error {
	body, err := transformCodebuddyBody(req.Body)
	if err != nil {
		return fmt.Errorf("transform codebuddy body: %w", err)
	}

	ctx := req.Ctx
	// Always forward as stream — CodeBuddy rejects stream=false
	resp, err := proxy.ForwardOpenAI(ctx, req.Client, req.Config, req.APIKey, body, true)
	if err != nil {
		return fmt.Errorf("ForwardCodebuddyCN upstream: %w", err)
	}
	defer resp.Body.Close()

	if req.IsStream {
		stallReader := proxy.NewStallReader(resp.Body, 0, "codebuddy-cn")
		defer stallReader.Close() // stops the shutdown watcher + stall timer
		return execSSEStream(w, stallReader, req)
	}
	// Client asked for non-stream, but we sent stream=true upstream, so the
	// upstream body is OpenAI-chat SSE. Re-aggregate the chunks into a single
	// JSON chat.completion response (mirrors the JS parseSSEToOpenAIResponse
	// path). If it isn't SSE (e.g. an upstream error JSON), pass it through.
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return fmt.Errorf("read codebuddy response: %w", err)
	}
	if converted, ok := sseToOpenAIJSON(data); ok {
		data = converted
	}
	return jsonResponse(req.Ctx, w, bytes.NewReader(data), req.TranslateResp, req.ResponseBuf)
}

// transformCodebuddyBody forces stream=true and handles reasoning params.
func transformCodebuddyBody(body []byte) ([]byte, error) {
	var reqMap map[string]interface{}
	if err := json.Unmarshal(body, &reqMap); err != nil {
		return nil, fmt.Errorf("parse body: %w", err)
	}

	// Force stream — CodeBuddy rejects non-stream (HTTP 400, code 11101)
	reqMap["stream"] = true

	// Handle reasoning_effort / reasoning_summary
	if eff, ok := reqMap["reasoning_effort"].(string); ok {
		switch eff {
		case "none", "off":
			// CodeBuddy gateway has no "none" — just omit
			delete(reqMap, "reasoning_effort")
		default:
			// Client explicitly asked for reasoning — mirror the CLI's
			// reasoning_summary so CodeBuddy surfaces the model's reasoning.
			reqMap["reasoning_summary"] = "auto"
		}
	}

	return json.Marshal(reqMap)
}

// sseToOpenAIJSON re-aggregates OpenAI chat-completions SSE chunks into a
// single chat.completion JSON object. Returns (nil, false) when raw is not
// SSE-shaped (e.g. an upstream error JSON that should pass through).
func sseToOpenAIJSON(raw []byte) ([]byte, bool) {
	var chunks []map[string]any
	var streamErr map[string]any
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "data:") {
			continue
		}
		payload := strings.TrimSpace(trimmed[5:])
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if e, ok := chunk["error"]; ok {
			streamErr, _ = e.(map[string]any)
			continue
		}
		chunks = append(chunks, chunk)
	}
	if streamErr != nil {
		b, _ := json.Marshal(map[string]any{"error": streamErr})
		return b, true
	}
	if len(chunks) == 0 {
		return nil, false
	}

	var contentParts, reasoningParts []string
	toolCallIdx := map[int]map[string]any{}
	var toolCalls []map[string]any
	finishReason := "stop"
	var usage any
	var first map[string]any

	for _, chunk := range chunks {
		if first == nil {
			first = chunk
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		choice, _ := choices[0].(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		if c, ok := delta["content"].(string); ok && c != "" {
			contentParts = append(contentParts, c)
		}
		if r, ok := delta["reasoning_content"].(string); ok && r != "" {
			reasoningParts = append(reasoningParts, r)
		}
		if fr, ok := choice["finish_reason"].(string); ok {
			finishReason = fr
		}
		if u, ok := chunk["usage"]; ok {
			usage = u
		}
		if tcs, ok := delta["tool_calls"].([]any); ok {
			for _, tcAny := range tcs {
				tc, _ := tcAny.(map[string]any)
				idx, _ := tc["index"].(float64)
				entry, ok := toolCallIdx[int(idx)]
				if !ok {
					entry = map[string]any{
						"id":       "",
						"type":     "function",
						"function": map[string]any{"name": "", "arguments": ""},
					}
					toolCallIdx[int(idx)] = entry
					toolCalls = append(toolCalls, entry)
				}
				if id, ok := tc["id"].(string); ok && id != "" {
					entry["id"] = id
				}
				if fn, ok := tc["function"].(map[string]any); ok {
					fEntry, _ := entry["function"].(map[string]any)
					if n, ok := fn["name"].(string); ok {
						fEntry["name"] = fEntry["name"].(string) + n
					}
					if a, ok := fn["arguments"].(string); ok {
						fEntry["arguments"] = fEntry["arguments"].(string) + a
					}
				}
			}
		}
	}

	msg := map[string]any{"role": "assistant"}
	content := strings.Join(contentParts, "")
	if content == "" {
		if len(toolCalls) > 0 {
			msg["content"] = nil
		} else {
			msg["content"] = ""
		}
	} else {
		msg["content"] = content
	}
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
	}
	if len(reasoningParts) > 0 {
		msg["reasoning_content"] = strings.Join(reasoningParts, "")
	}

	result := map[string]any{
		"id":      first["id"],
		"object":  "chat.completion",
		"created": first["created"],
		"model":   first["model"],
		"choices": []map[string]any{{
			"index":         0,
			"message":       msg,
			"finish_reason": finishReason,
		}},
	}
	if usage != nil {
		result["usage"] = usage
	}
	if result["id"] == nil {
		result["id"] = fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	}
	if result["created"] == nil {
		result["created"] = time.Now().Unix()
	}
	if result["model"] == nil {
		result["model"] = ""
	}
	b, err := json.Marshal(result)
	if err != nil {
		return nil, false
	}
	return b, true
}
