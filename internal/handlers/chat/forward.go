package chat

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
	internalproxy "9router/proxy/internal/proxy"
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
) error {
	// OpenAI-compat Gemini endpoints validate tool schemas as strictly as the
	// native one — sanitize tools so no unsupported JSON-Schema keyword reaches
	// them ("Invalid tool parameters" fix).
	if cfg.IsGeminiOpenAICompat() {
		if sanitized, serr := translator.SanitizeOpenAITools(body); serr == nil && sanitized != nil {
			body = sanitized
		}
	}
	resp, err := internalproxy.ForwardOpenAI(ctx, h.Client, cfg, apiKey, body, isStream)
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
		respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
		if err != nil {
			return fmt.Errorf("read upstream error body: %w", err)
		}
		return &upstreamError{StatusCode: resp.StatusCode, Body: respBody}
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
		stallReader := internalproxy.NewStallReader(resp.Body, 0, "upstream")
		bodyCloser = stallReader
		return h.handleStreamResponse(ctx, w, stallReader, translateResponse, start, metrics)
	}
	return h.handleJSONResponse(ctx, w, resp.Body, translateResponse, metrics)
}

// handleStreamResponse pipes SSE chunks from upstream to the client.
func (h *ChatHandler) handleStreamResponse(ctx context.Context, w http.ResponseWriter, upstream io.Reader, translate bool, startTime time.Time, metrics *streamMetrics) error {
	flusher := internalproxy.WriteSSEHeaders(w)

	if !translate {
		return internalproxy.SSECopy(w, upstream, flusher, func(chunk []byte) {
			if metrics.TTFT == 0 {
				metrics.TTFT = time.Since(startTime).Milliseconds()
			}
			metrics.ResponseBuf.Write(chunk)
		})
	}

	sessionKey := fmt.Sprintf("stream-%d", time.Now().UnixNano())
	defer translator.ClearStreamState(sessionKey)
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
		w.Write(translated)
		if flusher != nil {
			flusher.Flush()
		}
	})
	// A shutdown abort cuts the stream mid-way. End it with the same terminator
	// a natural finish emits, so the client sees a clean [DONE] instead of a
	// truncated stream (the stall reader already closed the upstream body).
	if shutdown.Fired() && !finished {
		w.Write([]byte("data: [DONE]\n\n"))
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

// handleJSONResponse forwards a non-streaming JSON response.
func (h *ChatHandler) handleJSONResponse(ctx context.Context, w http.ResponseWriter, upstream io.Reader, translate bool, metrics *streamMetrics) error {
	body, err := io.ReadAll(io.LimitReader(upstream, 10*1024*1024))
	if err != nil {
		return fmt.Errorf("read upstream response body: %w", err)
	}

	if metrics != nil {
		metrics.ResponseBuf.Write(body)
	}

	if !translate {
		// The !translate path serves both OpenAI bodies (/v1/chat/completions,
		// translateResponse hardcoded false) and Claude bodies (claude/anthropic
		// providers). ParseResponseUsage handles both formats.
		if usage := translator.ParseResponseUsage(body); usage != nil {
			translator.SetUsage(ctx, usage)
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
		return fmt.Errorf("%s", errMsg)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(translated)
	return nil
}
