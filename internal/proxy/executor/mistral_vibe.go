package executor

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/fastjson"
	"9router/proxy/internal/providers"
)

// The Mistral Vibe wire contract from github.com/Classic298/mistral-gateway.
// Every helper here is inert unless the connection explicitly sets
// usageLane = "vibe", so an ordinary Mistral API connection keeps the generic
// OpenAI-compatible path unchanged.

var mistralVibeEffortLevels = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

func isMistralVibeRequest(req *Request) bool {
	return req != nil && req.Provider == "mistral" && providers.IsMistralVibeConnection(req.ConnData)
}

func isGLMModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "glm")
}

// normalizeMistralVibeRequest applies the reference gateway's outbound fixes:
// alias resolution, OpenAI-field renames/removals, a real output budget, and
// the GLM tool-result effort bump. Bodies that do not parse are passed through
// untouched so the upstream error remains diagnosable.
func normalizeMistralVibeRequest(body []byte) []byte {
	if len(bytes.TrimSpace(body)) == 0 {
		return body
	}
	var chat map[string]any
	if err := fastjson.Unmarshal(body, &chat); err != nil {
		return body
	}
	changed := false

	model, _ := chat["model"].(string)
	if canonical := providers.CanonicalMistralVibeModel(model); canonical != model {
		chat["model"] = canonical
		model = canonical
		changed = true
	}

	// Mistral stores nothing: chat answers the field with 422, Responses with 400.
	if _, ok := chat["store"]; ok {
		delete(chat, "store")
		changed = true
	}

	// Responses requests only share the alias and store rules with chat. The
	// reference gateway returns before the chat-only renames and defaults, so
	// sending max_tokens/random_seed there would itself be a 422.
	if _, hasMessages := chat["messages"]; !hasMessages {
		if _, hasInput := chat["input"]; hasInput {
			if !changed {
				return body
			}
			out, err := fastjson.Marshal(chat)
			if err != nil {
				return body
			}
			return out
		}
	}

	stream, _ := chat["stream"].(bool)
	if stream {
		opts, _ := chat["stream_options"].(map[string]any)
		if opts == nil {
			opts = make(map[string]any, 1)
			chat["stream_options"] = opts
			changed = true
		}
		if included, _ := opts["include_usage"].(bool); !included {
			opts["include_usage"] = true
			changed = true
		}
	}

	// These OpenAI field names make Mistral answer 422.
	if value, ok := chat["max_completion_tokens"]; ok {
		delete(chat, "max_completion_tokens")
		if value != nil {
			chat["max_tokens"] = value
		}
		changed = true
	}
	if value, ok := chat["seed"]; ok {
		delete(chat, "seed")
		chat["random_seed"] = value
		changed = true
	}
	if _, ok := chat["user"]; ok {
		delete(chat, "user")
		changed = true
	}

	if _, ok := chat["max_tokens"]; !ok {
		// Without a budget Mistral caps GLM at 4096 tokens, less than its
		// reasoning alone. GLM therefore gets its full 128K output limit.
		budget := 32768
		if isGLMModel(model) {
			budget = 131072
		}
		chat["max_tokens"] = budget
		changed = true
	}

	messages, _ := chat["messages"].([]any)
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		// Mistral 422s when a reply is sent back with these fields intact.
		for _, field := range []string{"reasoning_content", "index"} {
			if _, ok := message[field]; ok {
				delete(message, field)
				changed = true
			}
		}
	}

	_, hasEffort := chat["reasoning_effort"]
	if !hasEffort && isGLMModel(model) && chat["tools"] != nil &&
		len(messages) > 0 {
		if last, ok := messages[len(messages)-1].(map[string]any); ok && last["role"] == "tool" {
			chat["reasoning_effort"] = "high"
			changed = true
		}
	}

	if !changed {
		return body
	}
	out, err := fastjson.Marshal(chat)
	if err != nil {
		return body
	}
	return out
}

// flattenMistralVibeContent reduces GLM's typed content parts to the plain
// string and reasoning_content that OpenAI-shaped clients expect.
func flattenMistralVibeContent(parts []any) (text string, reasoning string) {
	var texts, thoughts []string
	for _, raw := range parts {
		part, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch part["type"] {
		case "thinking":
			items, _ := part["thinking"].([]any)
			for _, item := range items {
				entry, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if s, ok := entry["text"].(string); ok {
					thoughts = append(thoughts, s)
				}
			}
		case "text":
			if s, ok := part["text"].(string); ok {
				texts = append(texts, s)
				continue
			}
			items, _ := part["text"].([]any)
			for _, item := range items {
				entry, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if s, ok := entry["text"].(string); ok {
					texts = append(texts, s)
				}
			}
		}
	}
	return strings.Join(texts, ""), strings.Join(thoughts, "")
}

// normalizeMistralVibeCompletion rewrites one unary Chat Completions body.
func normalizeMistralVibeCompletion(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"choices"`)) {
		return body
	}
	var response map[string]any
	if err := fastjson.Unmarshal(body, &response); err != nil {
		return body
	}
	choices, ok := response["choices"].([]any)
	if !ok {
		return body
	}
	changed := false
	for _, raw := range choices {
		choice, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		message, ok := choice["message"].(map[string]any)
		if !ok {
			continue
		}
		if parts, ok := message["content"].([]any); ok {
			text, reasoning := flattenMistralVibeContent(parts)
			message["content"] = text
			if reasoning != "" {
				message["reasoning_content"] = reasoning
			}
			changed = true
		}
		if calls, ok := message["tool_calls"].([]any); ok && len(calls) > 0 {
			if finish, _ := choice["finish_reason"].(string); finish == "stop" {
				choice["finish_reason"] = "tool_calls"
				changed = true
			}
		}
	}
	if !changed {
		return body
	}
	out, err := fastjson.Marshal(response)
	if err != nil {
		return body
	}
	return out
}

// mistralVibeStreamPatcher rewrites one SSE data payload. sawToolCall persists
// across chunks because the terminal chunk carries no tool-call deltas.
type mistralVibeStreamPatcher struct {
	sawToolCall bool
}

func (p *mistralVibeStreamPatcher) Process(payload []byte) []byte {
	if bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) ||
		!bytes.Contains(payload, []byte(`"choices"`)) {
		return payload
	}
	var chunk map[string]any
	if err := fastjson.Unmarshal(payload, &chunk); err != nil {
		return payload
	}
	choices, ok := chunk["choices"].([]any)
	if !ok {
		return payload
	}
	changed := false
	for _, raw := range choices {
		choice, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		delta, ok := choice["delta"].(map[string]any)
		if !ok {
			continue
		}
		if parts, ok := delta["content"].([]any); ok {
			text, reasoning := flattenMistralVibeContent(parts)
			delta["content"] = text
			if reasoning != "" {
				delta["reasoning_content"] = reasoning
			}
			changed = true
		}
		if calls, ok := delta["tool_calls"].([]any); ok && len(calls) > 0 {
			p.sawToolCall = true
		}
		if p.sawToolCall {
			if finish, _ := choice["finish_reason"].(string); finish == "stop" {
				choice["finish_reason"] = "tool_calls"
				changed = true
			}
		}
	}
	if !changed {
		return payload
	}
	out, err := fastjson.Marshal(chunk)
	if err != nil {
		return payload
	}
	return out
}

// mistralVibeSSEPatchReader applies the stream patcher to raw SSE before the
// Responses bridge consumes it. The passthrough lane uses SSECopy's patcher
// hook instead, so this wrapper exists only for Clients using /v1/responses.
type mistralVibeSSEPatchReader struct {
	src     io.Reader
	pending []byte
	out     []byte
	patcher *mistralVibeStreamPatcher
	eof     bool
}

func (r *mistralVibeSSEPatchReader) Read(p []byte) (int, error) {
	for len(r.out) == 0 && !r.eof {
		buf := make([]byte, 4096)
		n, err := r.src.Read(buf)
		if n > 0 {
			r.pending = append(r.pending, buf[:n]...)
		}
		if err != nil {
			if err != io.EOF {
				return 0, err
			}
			r.eof = true
			if len(r.pending) > 0 {
				r.out = append(r.out, r.patchLine(r.pending)...)
				r.pending = nil
			}
		}
		// An unterminated line stays pending for the next read. The cap only
		// guards against a malformed upstream that never sends a newline.
		for len(r.pending) > constants.MaxUpstreamBodyBytes {
			line := r.pending[:constants.MaxUpstreamBodyBytes]
			r.pending = r.pending[constants.MaxUpstreamBodyBytes:]
			r.out = append(r.out, line...)
		}
		for {
			idx := bytes.IndexByte(r.pending, '\n')
			if idx < 0 {
				break
			}
			line := r.pending[:idx+1]
			r.pending = r.pending[idx+1:]
			r.out = append(r.out, r.patchLine(line)...)
		}
	}
	if len(r.out) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.out)
	r.out = r.out[n:]
	return n, nil
}

func (r *mistralVibeSSEPatchReader) patchLine(line []byte) []byte {
	trimmed := bytes.TrimSuffix(bytes.TrimSuffix(line, []byte("\n")), []byte("\r"))
	if !bytes.HasPrefix(trimmed, []byte("data: ")) {
		return line
	}
	payload := r.patcher.Process(trimmed[len("data: "):])
	if bytes.Equal(payload, trimmed[len("data: "):]) {
		return line
	}
	out := make([]byte, 0, len(payload)+8)
	out = append(out, []byte("data: ")...)
	out = append(out, payload...)
	if bytes.HasSuffix(line, []byte("\r\n")) {
		out = append(out, '\r')
	}
	return append(out, '\n')
}

// mistralVibeSupportedEffort derives the levels a model accepts from the
// reference gateway's README table. Unknown models get no reasoning support,
// which matches the gateway's behaviour of dropping the field for them.
func mistralVibeSupportedEffort(model string) []string {
	lower := strings.ToLower(model)
	switch {
	case isGLMModel(lower):
		return []string{"low", "high", "max"}
	case strings.Contains(lower, "deepseek-v4"):
		return []string{"none", "low", "high", "xhigh", "max"}
	case strings.Contains(lower, "mistral-large"),
		strings.Contains(lower, "mistral-medium"),
		strings.Contains(lower, "mistral-small"):
		return []string{"none", "high"}
	default:
		return nil
	}
}

// snapMistralVibeReasoningEffort retries a 400/422 whose body names
// reasoning_effort. The error text is authoritative when it lists supported
// levels; otherwise the README table supplies them. It returns nil when the
// request cannot be usefully rewritten.
func snapMistralVibeReasoningEffort(body, detail []byte) ([]byte, bool) {
	lowerDetail := strings.ToLower(string(detail))
	if !strings.Contains(lowerDetail, "reasoning") {
		return nil, false
	}
	var chat map[string]any
	if err := fastjson.Unmarshal(body, &chat); err != nil {
		return nil, false
	}
	owner, key := chat, "reasoning_effort"
	if reasoning, ok := chat["reasoning"].(map[string]any); ok {
		owner, key = reasoning, "effort"
	}
	requested, ok := owner[key].(string)
	if !ok {
		return nil, false
	}

	switch {
	case strings.Contains(lowerDetail, "is not enabled"):
		delete(owner, key)
	case strings.Contains(lowerDetail, "is not supported"):
		suffix := lowerDetail
		if idx := strings.Index(suffix, "is not supported"); idx >= 0 {
			suffix = suffix[idx+len("is not supported"):]
		}
		supported := make([]string, 0, len(mistralVibeEffortLevels))
		for _, level := range mistralVibeEffortLevels {
			if strings.Contains(suffix, "'"+level+"'") {
				supported = append(supported, level)
			}
		}
		if len(supported) == 0 {
			model, _ := chat["model"].(string)
			supported = mistralVibeSupportedEffort(providers.CanonicalMistralVibeModel(model))
		}
		if len(supported) == 0 {
			delete(owner, key)
			break
		}
		closest, ok := nearestMistralVibeEffort(requested, supported)
		if !ok {
			return nil, false
		}
		owner[key] = closest
	default:
		return nil, false
	}

	out, err := fastjson.Marshal(chat)
	if err != nil {
		return nil, false
	}
	return out, true
}

func nearestMistralVibeEffort(requested string, supported []string) (string, bool) {
	rank := -1
	for i, level := range mistralVibeEffortLevels {
		if level == requested {
			rank = i
			break
		}
	}
	if rank < 0 {
		return "", false
	}
	low := requested == "none" || requested == "minimal" || requested == "low"
	best, bestDistance := "", -1
	for i := 0; i < len(supported); i++ {
		candidate := supported[i]
		if !low {
			candidate = supported[len(supported)-1-i]
		}
		for j, level := range mistralVibeEffortLevels {
			if level != candidate {
				continue
			}
			distance := j - rank
			if distance < 0 {
				distance = -distance
			}
			if bestDistance < 0 || distance < bestDistance {
				best, bestDistance = candidate, distance
			}
			break
		}
	}
	return best, bestDistance >= 0
}

// ForwardMistral keeps the generic OpenAI path for normal Mistral API keys and
// layers the Vibe wire contract on top only for usageLane = "vibe".
func ForwardMistral(w http.ResponseWriter, req *Request) error {
	if !isMistralVibeRequest(req) {
		return ForwardOpenAI(w, req)
	}
	// Keep routing, usage and logs on the canonical id even when a client used
	// one of the gateway-only alias names.
	req.ModelName = providers.CanonicalMistralVibeModel(req.ModelName)
	req.Body = normalizeMistralVibeRequest(req.Body)
	req.OnUpstreamError = func(status int, detail []byte) ([]byte, bool) {
		if status != http.StatusBadRequest && status != http.StatusUnprocessableEntity {
			return nil, false
		}
		return snapMistralVibeReasoningEffort(req.Body, detail)
	}
	return ForwardOpenAI(w, req)
}
