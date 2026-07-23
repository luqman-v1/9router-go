package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"9router/proxy/internal/log"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/providers"
	internalproxy "9router/proxy/internal/proxy"
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
		return h.handleStreamResponse(w, stallReader, translateResponse, start, metrics)
	}
	return h.handleJSONResponse(ctx, w, resp.Body, translateResponse, metrics)
}

// handleStreamResponse pipes SSE chunks from upstream to the client.
func (h *ChatHandler) handleStreamResponse(w http.ResponseWriter, upstream io.Reader, translate bool, startTime time.Time, metrics *streamMetrics) error {
	flusher := internalproxy.WriteSSEHeaders(w)

	if !translate {
		return internalproxy.SSECopy(w, upstream, flusher, func(chunk []byte) {
			if metrics.TTFT == 0 {
				metrics.TTFT = time.Since(startTime).Milliseconds()
			}
			metrics.ResponseBuf.Write(chunk)
		})
	}

	return internalproxy.ScanStream(upstream, func(chunk []byte) {
		translated, err := translator.TranslateOpenAIToClaudeStream(chunk)
		if err != nil {
			log.Error("stream", "translate error", "error", err)
			return
		}
		if translated == nil {
			return
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
}

// handleJSONResponse forwards a non-streaming JSON response.
func (h *ChatHandler) handleJSONResponse(ctx context.Context, w http.ResponseWriter, upstream io.Reader, translate bool, metrics *streamMetrics) error {
	body, err := io.ReadAll(upstream)
	if err != nil {
		return fmt.Errorf("read upstream response body: %w", err)
	}

	if metrics != nil {
		metrics.ResponseBuf.Write(body)
	}

	if !translate {
		var raw struct {
			Usage *translator.OpenAIUsage `json:"usage"`
		}
		if json.Unmarshal(body, &raw) == nil && raw.Usage != nil {
			translator.SetUsage(ctx, raw.Usage)
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
