package executor

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"9router/proxy/internal/proxy"
)

// ForwardOpenAI sends an OpenAI-format request and writes the response.
func ForwardOpenAI(w http.ResponseWriter, req *Request) error {
	resp, err := proxy.ForwardOpenAI(req.Client, req.Config, req.APIKey, req.Body, req.IsStream)
	if err != nil {
		return fmt.Errorf("ForwardOpenAI upstream: %w", err)
	}
	defer resp.Body.Close()
	if req.IsStream {
		return sseStream(w, resp.Body, req.TranslateResp, time.Now(), nil, nil)
	}
	return jsonResponse(w, resp.Body, req.TranslateResp)
}

// sseStream pipes SSE chunks to client with optional format translation.
func sseStream(w http.ResponseWriter, upstream io.Reader, translate bool, startTime time.Time, ttft *int64, buf *stringBuilder) error {
	// delegates to proxyhandlers
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
				if buf != nil { buf.Write(b[:n]) }
				w.Write(b[:n])
				if flusher != nil { flusher.Flush() }
			}
			if err != nil { break }
		}
		return nil
	}

	flusher, _ := w.(http.Flusher)
	return proxy.ScanStream(upstream, func(chunk []byte) {
		w.Write(chunk)
		w.Write([]byte("\n\n"))
		if flusher != nil { flusher.Flush() }
	})
}

// jsonResponse writes the upstream JSON response.
// TODO: When translate==true, call Claude translation.
func jsonResponse(w http.ResponseWriter, upstream io.Reader, translate bool) error {
	body, err := io.ReadAll(upstream)
	if err != nil { return fmt.Errorf("read upstream response: %w", err) }
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(body)
	return nil
}

type stringBuilder struct { b []byte }
func (s *stringBuilder) Write(p []byte) (int, error) { s.b = append(s.b, p...); return len(p), nil }
func (s *stringBuilder) String() string { return string(s.b) }
