package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"9router/proxy/internal/log"
	"9router/proxy/internal/proxy"
	"9router/proxy/internal/shutdown"
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
	return sseStream(w, upstream, req.TranslateResp, startTime, req.TTFT, req.ResponseBuf, req.Ctx)
}

// sseStream pipes SSE chunks to client with optional format translation.
func sseStream(w http.ResponseWriter, upstream io.Reader, translate bool, startTime time.Time, ttft *int64, buf io.Writer, ctx context.Context) error {
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

	sessionKey := fmt.Sprintf("stream-%d", time.Now().UnixNano())
	defer translator.ClearStreamState(sessionKey)
	finished := false
	err := proxy.ScanStream(upstream, func(chunk []byte) {
		translated, err := translator.TranslateOpenAIToClaudeStreamSession(sessionKey, chunk)
		if err != nil {
			log.Error("executor", "translate error", "error", err)
			return
		}
		if translated == nil {
			return
		}
		if bytes.Contains(translated, []byte("[DONE]")) {
			finished = true
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
	// Same shutdown terminator as the chat path: end with [DONE] on abort.
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

// jsonResponse writes the upstream JSON response with optional translation.
func jsonResponse(ctx context.Context, w http.ResponseWriter, upstream io.Reader, translate bool, buf io.Writer) error {
	body, err := io.ReadAll(io.LimitReader(upstream, 10*1024*1024))
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

	if usage := translator.ParseResponseUsage(body); usage != nil {
		if ctx != nil {
			translator.SetUsage(ctx, usage)
		} else {
			translator.SetLastUsage(usage)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(body)
	return nil
}
