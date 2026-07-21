package executor

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"bytes"
	"9router/proxy/internal/proxy"
	"9router/proxy/internal/translator"
)

// ForwardOpenAI sends an OpenAI-format request and writes the response.
func ForwardOpenAI(w http.ResponseWriter, req *Request) error {
	resp, err := proxy.ForwardOpenAI(req.Client, req.Config, req.APIKey, req.Body, req.IsStream)
	if err != nil {
		return fmt.Errorf("ForwardOpenAI upstream: %w", err)
	}
	defer resp.Body.Close()
	if req.IsStream {
		resp.Body = proxy.NewStallReader(resp.Body, 0, "openai")
	return sseStream(w, resp.Body, req.TranslateResp, time.Now(), nil, nil)
	}
	return jsonResponse(w, resp.Body, req.TranslateResp)
}

// sseStream pipes SSE chunks to client with optional format translation.
func sseStream(w http.ResponseWriter, upstream io.Reader, translate bool, startTime time.Time, ttft *int64, buf *bytes.Buffer) error {
	flusher := proxy.WriteSSEHeaders(w)

	if !translate {
		return proxy.SSECopy(w, upstream, flusher, func(chunk []byte) {
			if ttft != nil && *ttft == 0 {
				*ttft = time.Since(startTime).Milliseconds()
			}
			if buf != nil { buf.Write(chunk) }
		})
	}

	return proxy.ScanStream(upstream, func(chunk []byte) {
		translated, err := translator.TranslateOpenAIToClaudeStream(chunk)
		if err != nil {
			log.Printf("[executor] TranslateOpenAIToClaudeStream error: %v", err)
			return
		}
		if translated == nil {
			return
		}
		if ttft != nil && *ttft == 0 {
			*ttft = time.Since(startTime).Milliseconds()
		}
		if buf != nil { buf.Write(translated) }
		w.Write(translated)
		if flusher != nil { flusher.Flush() }
	})
}

// jsonResponse writes the upstream JSON response with optional translation.
func jsonResponse(w http.ResponseWriter, upstream io.Reader, translate bool) error {
	body, err := io.ReadAll(upstream)
	if err != nil { return fmt.Errorf("read upstream response: %w", err) }

	if translate {
		translated, usage, err := translator.TranslateOpenAIToClaude(body)
		if err == nil && usage != nil {
			translator.SetLastUsage(usage)
		}
		if err != nil || translated == nil {
			log.Printf("[executor] TranslateOpenAIToClaude error: %v", err)
			// Fall back to original response
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write(body)
			return nil
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(translated)
		return nil
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(body)
	return nil
}

