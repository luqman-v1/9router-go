package handlers

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
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
	req, err := http.NewRequest(http.MethodPost, cfg.BaseURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create upstream request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	if !cfg.NoAuth {
		switch cfg.AuthScheme {
		case "bearer":
			req.Header.Set(cfg.AuthHeader, "Bearer "+apiKey)
		case "raw":
			req.Header.Set(cfg.AuthHeader, apiKey)
		default:
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
	}

	for k, v := range cfg.StaticHeaders {
		req.Header.Set(k, v)
	}

	if isStream {
		req.Header.Set("Accept", "text/event-stream")
	}

	resp, err := h.Client.Do(req)
	if err != nil {
		return fmt.Errorf("upstream request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return &upstreamError{StatusCode: resp.StatusCode, Body: errBody}
	}

	start := time.Now()
	if metrics == nil {
		metrics = &streamMetrics{}
	}
	if isStream {
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
	return proxy.ScanStream(upstream, func(chunk []byte) {
		translated, err := translator.TranslateOpenAIToClaudeStream(chunk)
		if err != nil || translated == nil {
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
		return fmt.Errorf("failed to read upstream response: %w", err)
	}

	if !translate {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(body)
		return nil
	}

	translated, err := translator.TranslateOpenAIToClaudeStream(body)
	if err != nil || translated == nil {
		errMsg := "failed to translate upstream response to Claude format"
		if err != nil {
			errMsg = errMsg + ": " + err.Error()
		}
		handlerutil.WriteJSONError(w, http.StatusBadGateway, errMsg)
		return nil
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(translated)
	return nil
}
