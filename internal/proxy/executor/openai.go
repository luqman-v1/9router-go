package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"9router/proxy/internal/log"
	"net/http"
	"time"

	"9router/proxy/internal/proxy"
	"9router/proxy/internal/translator"
)

// ForwardOpenAI sends an OpenAI-format request and writes the response.
func ForwardOpenAI(w http.ResponseWriter, req *Request) error {
	resp, err := proxy.ForwardOpenAI(req.Ctx, req.Client, req.Config, req.APIKey, req.Body, req.IsStream)
	if err != nil {
		return fmt.Errorf("ForwardOpenAI upstream: %w", err)
	}

	var bodyCloser io.Closer = resp.Body
	defer func() {
		if bodyCloser != nil {
			bodyCloser.Close()
		}
	}()

	if req.IsStream {
		stallReader := proxy.NewStallReader(resp.Body, 0, "openai")
		bodyCloser = stallReader
		return execSSEStream(w, stallReader, req)
	}
	return jsonResponse(req.Ctx, w, resp.Body, req.TranslateResp, req.ResponseBuf)
}

func execSSEStream(w http.ResponseWriter, upstream io.Reader, req *Request) error {
	startTime := req.StartTime
	if startTime.IsZero() {
		startTime = time.Now()
	}
	return sseStream(w, upstream, req.TranslateResp, startTime, req.TTFT, req.ResponseBuf)
}

// sseStream pipes SSE chunks to client with optional format translation.
func sseStream(w http.ResponseWriter, upstream io.Reader, translate bool, startTime time.Time, ttft *int64, buf io.Writer) error {
	flusher := proxy.WriteSSEHeaders(w)

	if !translate {
		return proxy.SSECopy(w, upstream, flusher, func(chunk []byte) {
			if ttft != nil && *ttft == 0 {
				*ttft = time.Since(startTime).Milliseconds()
			}
			if buf != nil {
				buf.Write(chunk)
			}
		})
	}

	return proxy.ScanStream(upstream, func(chunk []byte) {
		translated, err := translator.TranslateOpenAIToClaudeStream(chunk)
		if err != nil {
			log.Error("executor", "translate error", "error", err)
			return
		}
		if translated == nil {
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

// jsonResponse writes the upstream JSON response with optional translation.
func jsonResponse(ctx context.Context, w http.ResponseWriter, upstream io.Reader, translate bool, buf io.Writer) error {
	body, err := io.ReadAll(upstream)
	if err != nil {
		return fmt.Errorf("read upstream response: %w", err)
	}

	if buf != nil {
		buf.Write(body)
	}

	if translate {
		translated, usage, err := translator.TranslateOpenAIToClaude(body)
		if err == nil && usage != nil {
			if ctx != nil {
				translator.SetUsage(ctx, usage)
			} else {
				translator.SetLastUsage(usage)
			}
		}
		if err != nil || translated == nil {
			log.Error("executor", "json translate error", "error", err)
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

	var raw struct {
		Usage *translator.OpenAIUsage `json:"usage"`
	}
	if json.Unmarshal(body, &raw) == nil && raw.Usage != nil {
		if ctx != nil {
			translator.SetUsage(ctx, raw.Usage)
		} else {
			translator.SetLastUsage(raw.Usage)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(body)
	return nil
}

