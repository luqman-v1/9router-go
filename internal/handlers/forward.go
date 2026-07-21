package handlers

import (
	"fmt"
	"io"
	"log"
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
	w http.ResponseWriter,
	cfg *providers.ProviderConfig,
	apiKey string,
	body []byte,
	isStream bool,
	translateResponse bool,
	metrics *streamMetrics,
) error {
	resp, err := internalproxy.ForwardOpenAI(h.Client, cfg, apiKey, body, isStream)
	if err != nil {
		return fmt.Errorf("forward to upstream: %w", err)
	}
	defer resp.Body.Close()

	start := time.Now()
	if metrics == nil {
		metrics = &streamMetrics{}
	}
	if isStream {
		contentType := resp.Header.Get("Content-Type")
		if !strings.HasPrefix(strings.ToLower(contentType), "text/event-stream") {
			// Upstream returned non-streaming response (e.g. JSON error with 200 OK)
			log.Printf("[stream_warning] upstream returned non-stream Content-Type: %s for stream request", contentType)
			return h.handleJSONResponse(w, resp.Body, translateResponse)
		}
		return h.handleStreamResponse(w, resp.Body, translateResponse, start, metrics)
	}
	return h.handleJSONResponse(w, resp.Body, translateResponse)
}

// handleStreamResponse pipes SSE chunks from upstream to the client.
func (h *ChatHandler) handleStreamResponse(w http.ResponseWriter, upstream io.Reader, translate bool, startTime time.Time, metrics *streamMetrics) error {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	if !translate {
		flusher, _ := w.(http.Flusher)
		buf := make([]byte, 4096)
		for {
			n, err := upstream.Read(buf)
			if n > 0 {
				if metrics.ttft == 0 {
					metrics.ttft = time.Since(startTime).Milliseconds()
				}
				metrics.responseBuf.Write(buf[:n])
				w.Write(buf[:n])
				if flusher != nil {
					flusher.Flush()
				}
			}
			if err != nil {
				break
			}
		}
		return nil
	}

	flusher, _ := w.(http.Flusher)
	return internalproxy.ScanStream(upstream, func(chunk []byte) {
		translated, err := translator.TranslateOpenAIToClaudeStream(chunk)
		if err != nil {
			log.Printf("[stream_error] TranslateOpenAIToClaudeStream error: %v", err)
			return
		}
		if translated == nil {
			return
		}
		if metrics.ttft == 0 {
			metrics.ttft = time.Since(startTime).Milliseconds()
		}
		metrics.responseBuf.Write(translated)
		w.Write(translated)
		if flusher != nil {
			flusher.Flush()
		}
	})
}

// handleJSONResponse forwards a non-streaming JSON response.
func (h *ChatHandler) handleJSONResponse(w http.ResponseWriter, upstream io.Reader, translate bool) error {
	body, err := io.ReadAll(upstream)
	if err != nil {
		return fmt.Errorf("read upstream response body: %w", err)
	}

	if !translate {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(body)
		return nil
	}

	translated, usage, err := translator.TranslateOpenAIToClaude(body)
	if err == nil && usage != nil {
		translator.SetLastUsage(usage)
	}
	if err != nil || translated == nil {
		errMsg := "failed to translate upstream response to Claude format"
		if err != nil {
			errMsg = errMsg + ": " + err.Error()
		}
		log.Printf("[json_error] %s", errMsg)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, errMsg)
		return fmt.Errorf("%s", errMsg)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(translated)
	return nil
}
