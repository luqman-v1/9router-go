// Package proxyhandlers provides standalone handler functions that write responses
// directly to http.ResponseWriter. Each function handles the full forward + response cycle.
package proxyhandlers

import (
	"io"
	"net/http"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/proxy"
	"9router/proxy/internal/translator"
)

// SSEStream pipes SSE chunks from upstream to client with optional translation.
func SSEStream(w http.ResponseWriter, upstream io.Reader, translate bool, startTime time.Time, ttft *int64, buf *stringBuilder) error {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	if !translate {
		flusher, _ := w.(http.Flusher)
		b := make([]byte, 4096)
		for {
			n, err := upstream.Read(b)
			if n > 0 {
				if ttft != nil && *ttft == 0 {
					*ttft = time.Since(startTime).Milliseconds()
				}
				if buf != nil {
					buf.Write(b[:n])
				}
				w.Write(b[:n])
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
		if ttft != nil && *ttft == 0 {
			*ttft = time.Since(startTime).Milliseconds()
		}
		if buf != nil {
			buf.Write(translated)
		}
		w.Write(translated)
		if flusher != nil {
			flusher.Flush()
		}
	})
}

// JSONResponse forwards a non-streaming JSON response with optional translation.
func JSONResponse(w http.ResponseWriter, upstream io.Reader, translate bool) error {
	body, err := io.ReadAll(upstream)
	if err != nil {
		return err
	}

	if !translate {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(body)
		return nil
	}

	translated, err := translator.TranslateOpenAIToClaude(body)
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

// stringBuilder is a simple io.Writer for accumulating response bytes.
type stringBuilder struct {
	b []byte
}

func (s *stringBuilder) Write(p []byte) (int, error) {
	s.b = append(s.b, p...)
	return len(p), nil
}

func (s *stringBuilder) String() string { return string(s.b) }
