package chat

import (
	"bytes"
	"context"
	json "9router/proxy/internal/fastjson"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/guardrails"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/semanticcache"
	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
	internalproxy "9router/proxy/internal/proxy"
	"9router/proxy/internal/proxy/executor"
	"9router/proxy/internal/shutdown"
	"9router/proxy/internal/translator"
)

// forwardRequest sends the request to the upstream provider and streams/pipes the response.
func (h *ChatHandler) forwardRequest(
	ctx context.Context,
	w http.ResponseWriter,
	cfg *providers.ProviderConfig,
	apiKey string,
	body []byte,
	isStream bool,
	translateResponse bool,
	metrics *streamMetrics,
	// httpClient is the connection's client, proxy pool included. It is a
	// parameter because falling back to h.Client here would dial the provider
	// directly for every provider without a custom executor — the assigned
	// pool would look bound in the dashboard and never be used.
	httpClient *http.Client,
) error {
	// A nil client means the caller had no connection context; the shared one
	// is the only sensible default.
	if httpClient == nil {
		httpClient = h.Client
	}
	// OpenAI-compat Gemini endpoints validate tool schemas as strictly as the
	// native one — sanitize tools so no unsupported JSON-Schema keyword reaches
	// them ("Invalid tool parameters" fix).
	if cfg.IsGeminiOpenAICompat() {
		if sanitized, serr := translator.SanitizeOpenAITools(body); serr == nil && sanitized != nil {
			body = sanitized
		}
	}
	// OpenRouter strict pattern validation (PR #3665): strip malformed regex patterns
	if strings.Contains(cfg.BaseURL, "openrouter.ai") {
		var bodyMap map[string]any
		if err := json.Unmarshal(body, &bodyMap); err == nil {
			if tools, ok := bodyMap["tools"].([]any); ok && len(tools) > 0 {
				if normalized, removed := translator.NormalizeToolSchemasForProvider("openrouter", tools); removed > 0 {
					bodyMap["tools"] = normalized
					log.Debug("tool_schema", "openrouter stripped invalid patterns", "removed", removed)
					if newBody, err := json.Marshal(bodyMap); err == nil {
						body = newBody
					}
				}
			}
		}
	}
	resp, err := internalproxy.ForwardOpenAI(ctx, httpClient, cfg, apiKey, body, isStream)
	if err != nil {
		return fmt.Errorf("forward to upstream: %w", err)
	}

	var bodyCloser io.Closer = resp.Body
	defer func() {
		if bodyCloser != nil {
			bodyCloser.Close()
		}
	}()

	if resp.StatusCode != http.StatusOK {
		respBody, err := io.ReadAll(io.LimitReader(resp.Body, constants.UpstreamErrLimit))
		if err != nil {
			return fmt.Errorf("read upstream error body: %w", err)
		}
		return &upstreamError{StatusCode: resp.StatusCode, Body: respBody, Header: resp.Header}
	}

	start := time.Now()
	if metrics == nil {
		metrics = &streamMetrics{}
	}
	if isStream {
		contentType := resp.Header.Get("Content-Type")
		if !strings.HasPrefix(strings.ToLower(contentType), "text/event-stream") {
			// Upstream returned non-streaming response (e.g. JSON error with 200 OK)
			log.Warn("stream", "non-stream response", "contentType", contentType)
			return h.handleJSONResponse(ctx, w, resp.Body, translateResponse, metrics)
		}
		// Wrap with SSE stall detection
		stallReader := internalproxy.NewStallReaderWithContext(ctx, resp.Body, 0, "upstream")
		bodyCloser = stallReader
		return h.handleStreamResponse(ctx, w, stallReader, translateResponse, start, metrics)
	}
	return h.handleJSONResponse(ctx, w, resp.Body, translateResponse, metrics)
}

// handleStreamResponse pipes SSE chunks from upstream to the client.
func (h *ChatHandler) handleStreamResponse(ctx context.Context, w http.ResponseWriter, upstream io.Reader, translate bool, startTime time.Time, metrics *streamMetrics) error {
	// A /v1/responses client needs Responses events, and translate marks a
	// Claude client, so this branch is decided by the client format alone. It
	// runs before the header write because the bridge owns its own writer.
	if translator.NeedsResponsesBridge(ctx) {
		// The bridge defers response.completed until the upstream's usage
		// trailer arrives, so once that wait begins the generic six-minute
		// stall window is far too generous: an upstream that never sends the
		// trailer would hold the client that long. One stall reader sized to
		// the deferral bound ends the wait in step with the watchdog.
		stream := upstream
		if pending := executor.PendingCompletionFlushTimeout; pending > 0 {
			if body, ok := upstream.(io.ReadCloser); ok {
				stream = internalproxy.NewStallReaderWithContext(ctx, body, pending, "upstream pending completion")
			}
		}
		return executor.StreamChatToResponses(ctx, w, stream, startTime, &metrics.TTFT, &metrics.ResponseBuf)
	}

	hw := internalproxy.NewHeartbeatWriter(ctx, w, 0)
	defer hw.Close()
	flusher := internalproxy.WriteSSEHeaders(hw)

	if !translate {
		err := internalproxy.ScanStream(upstream, func(payload []byte) {
			if metrics.TTFT == 0 {
				metrics.TTFT = time.Since(startTime).Milliseconds()
			}
			frame := append([]byte("data: "), payload...)
			frame = append(frame, '\n', '\n')
			metrics.ResponseBuf.Write(frame)
			_, _ = hw.Write(frame)
			if flusher != nil {
				flusher.Flush()
			}
			if usage := translator.ParseResponseUsage(payload); usage != nil {
				translator.SetUsage(ctx, usage)
			}
		})
		if err != nil {
			return err
		}
		return nil
	}

	sessionKey := fmt.Sprintf("stream-%d", time.Now().UnixNano())
	// Seed with requested model so message_start echoes client's model (decolua/9router#3693)
	if reqModel := translator.RequestedModelFromContext(ctx); reqModel != "" {
		translator.SeedStreamState(sessionKey, reqModel)
	}
	defer func() {
		if endChunk := translator.EnsureStreamClosed(sessionKey); len(endChunk) > 0 {
			hw.Write(endChunk)
			if flusher != nil {
				flusher.Flush()
			}
		}
		translator.ClearStreamState(sessionKey)
	}()
	finished := false
	err := internalproxy.ScanStream(upstream, func(chunk []byte) {
		translated, err := translator.TranslateOpenAIToClaudeStreamSession(sessionKey, chunk)
		if err != nil {
			log.Error("stream", "translate error", "error", err)
			return
		}
		if translated == nil {
			return
		}
		if bytes.Contains(translated, []byte("[DONE]")) {
			finished = true
		}
		if metrics.TTFT == 0 {
			metrics.TTFT = time.Since(startTime).Milliseconds()
		}
		metrics.ResponseBuf.Write(translated)
		hw.Write(translated)
		if flusher != nil {
			flusher.Flush()
		}
	})
	// A shutdown abort cuts the stream mid-way. End it with the same terminator
	// a natural finish emits, so the client sees a clean [DONE] instead of a
	// truncated stream (the stall reader already closed the upstream body).
	if shutdown.Fired() && !finished {
		hw.Write([]byte("data: [DONE]\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}
	// Pull actual accumulated usage (incl. cached tokens) out of the session so
	// the log sees real numbers instead of the fallback estimate.
	if usage := translator.GetStreamUsage(sessionKey); usage != nil {
		translator.SetUsage(ctx, usage)
	}
	return err
}

// sseToClaudeJSON aggregates OpenAI SSE chunks into a single chat.completion JSON for forced-SSE handling.
// Port of executor/codebuddy.go sseToOpenAIJSON for chatCore forced-SSE fix (decolua/9router#3683).
func sseToClaudeJSON(raw []byte) ([]byte, bool) {
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
					if n, ok := fn["name"].(string); ok && n != "" {
						if existing, _ := fEntry["name"].(string); existing == "" {
							fEntry["name"] = n
						} else if existing != n && !strings.Contains(existing, n) {
							fEntry["name"] = existing + n
						}
					}
					if a, ok := fn["arguments"].(string); ok && a != "" {
						existing, _ := fEntry["arguments"].(string)
						if existing == "" {
							fEntry["arguments"] = a
						} else if existing != a && !strings.Contains(existing, a) {
							if len(a) > 0 && !strings.HasSuffix(existing, a) {
								fEntry["arguments"] = existing + a
							}
						}
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
	if usage == nil {
		usage = map[string]any{"prompt_tokens": 0, "completion_tokens": len(content) / 4}
	}
	id := "chatcmpl-0"
	if first != nil {
		if v, ok := first["id"].(string); ok && v != "" {
			id = v
		}
	}
	model := "gpt-4o"
	if first != nil {
		if v, ok := first["model"].(string); ok && v != "" {
			model = v
		}
	}
	created := int64(0)
	if first != nil {
		if v, ok := first["created"].(float64); ok {
			created = int64(v)
		}
	}
	resp := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       msg,
			"finish_reason": finishReason,
		}},
		"usage": usage,
	}
	b, _ := json.Marshal(resp)
	return b, true
}

// handleJSONResponse forwards a non-streaming JSON response.
func (h *ChatHandler) handleJSONResponse(ctx context.Context, w http.ResponseWriter, upstream io.Reader, translate bool, metrics *streamMetrics) error {
	body, err := io.ReadAll(io.LimitReader(upstream, constants.MaxUpstreamBodyBytes))
	if err != nil {
		return fmt.Errorf("read upstream response body: %w", err)
	}

	body = translator.UnwrapClineEnvelope(body)

	// An SSE-only upstream ignores `stream:false` and answers with an event
	// stream anyway. Fold it into one chat.completion before validating, so
	// a stream that never carried a completion is a failure the fallback
	// layer can act on rather than a 200 the client reads as an empty
	// answer (port of executor codebuddy.go sseToOpenAIJSON, #3683).
	if internalproxy.LooksLikeSSE(body) {
		folded, ok := sseToClaudeJSON(body)
		if !ok {
			return internalproxy.UpstreamFailure(http.StatusBadGateway, internalproxy.NoCompletionInStream)
		}
		body = folded
	}

	// A 200 that carries no completion (blank body, HTML error page, a
	// `{"error": ...}` envelope, a choice with empty content) reads to every
	// layer above as a served turn, which both ends combo fallback and
	// clears the account cooldown. Report it as the 502 it is.
	if err := internalproxy.EmptyUpstreamError(body); err != nil {
		return err
	}

	if metrics != nil {
		metrics.ResponseBuf.Write(body)
	}

	// Outbound guardrails: the last point before the answer reaches the client.
	// It sits after the empty-response check so a blocked body is not mistaken
	// for a provider failure, and before the translate step so the policy sees
	// the model's own words rather than a reshaped envelope.
	filtered, gerr := guardrails.ApplyBuffered(ctx, body)
	if gerr != nil {
		return guardrailBlockedError()
	}
	body = filtered

	// A /v1/responses client on a Chat Completions upstream needs the answer in
	// the Responses shape; translate marks a Claude client, which it is not.
	if translator.NeedsResponsesBridge(ctx) {
		return h.respondAsResponses(ctx, w, body)
	}

	if !translate {
		// The !translate path serves both OpenAI bodies (/v1/chat/completions,
		// translateResponse hardcoded false) and Claude bodies (claude/anthropic
		// providers). ParseResponseUsage handles both formats.
		if usage := translator.ParseResponseUsage(body); usage != nil {
			translator.SetUsage(ctx, usage)
		}
		if h.SemanticCache != nil && h.SemanticCache.Enabled() {
			if cachedReq := semanticcache.CachedRequestFromContext(ctx); cachedReq != nil {
				_ = h.SemanticCache.Store(ctx, cachedReq, body, "application/json")
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(body)
		return nil
	}

	translated, usage, err := translator.TranslateOpenAIToClaude(body)
	if err == nil && usage != nil {
		translator.SetUsage(ctx, usage)
	}
	if err != nil || translated == nil {
		errMsg := "failed to translate upstream response to Claude format"
		if err != nil {
			errMsg = errMsg + ": " + err.Error()
		}
		log.Error("json", "translate error", "msg", errMsg)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, errMsg)
		return errors.New(errMsg)
	}
	if h.SemanticCache != nil && h.SemanticCache.Enabled() {
		if cachedReq := semanticcache.CachedRequestFromContext(ctx); cachedReq != nil {
			_ = h.SemanticCache.Store(ctx, cachedReq, translated, "application/json")
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(translated)
	return nil
}

// respondAsResponses writes a non-streaming Chat Completions body to a
// /v1/responses client in the Response shape it expects. A body the converter
// rejects is relayed unchanged rather than dropped: a body in the wrong shape
// still lets the client report the failure, an empty 200 tells it nothing.
func (h *ChatHandler) respondAsResponses(ctx context.Context, w http.ResponseWriter, body []byte) error {
	// A Responses client that asked for a single JSON can still be handed an
	// SSE stream when the provider forces one (Codex does). Aggregate it
	// first rather than answering a client that expects JSON with a stream.
	if internalproxy.LooksLikeSSE(body) {
		if aggregated, ok := sseToClaudeJSON(body); ok {
			body = aggregated
		}
	}
	converted, err := translator.ChatResponseToResponses(body)
	if err == nil {
		if usage := translator.ParseResponseUsage(body); usage != nil {
			translator.SetUsage(ctx, usage)
		}
		body = converted
	} else {
		log.Error("chat", "responses json translate error", "error", err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(body)
	return nil
}
