package handlers

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"time"
)

var proxyBase = "http://localhost:20130"

// FetchRouter sends a request body to the 9router proxy endpoint and returns the response.
func FetchRouter(ctx context.Context, body []byte, endpoint string, headers http.Header, apiKey string) (*http.Response, error) {
	url := proxyBase + endpoint
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	// Forward original headers
	if auth := headers.Get("Authorization"); auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if ct := headers.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if ua := headers.Get("User-Agent"); ua != "" {
		req.Header.Set("User-Agent", ua)
	}

	client := &http.Client{Timeout: 5 * time.Minute}
	return client.Do(req)
}

// PipeSSE copies streaming SSE chunks from upstream to the mitm client response.
func PipeSSE(upstream *http.Response, w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(upstream.StatusCode)

	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 64*1024)
	for {
		n, err := upstream.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return fmt.Errorf("write SSE chunk: %w", werr)
			}
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

// PipeJSON copies a non-streaming JSON response from upstream to the mitm client.
func PipeJSON(upstream *http.Response, w http.ResponseWriter) error {
	body, err := io.ReadAll(upstream.Body)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(upstream.StatusCode)
	w.Write(body)
	return nil
}

// SendError writes a JSON error response to the mitm client.
//
// The message is encoded by json.Marshal, never interpolated into a template: a
// message carrying a quote or a backslash (a URL that failed to parse, a Windows
// path) would otherwise close the JSON string early and leave a bare token
// behind, which a JavaScript client reports as
// `JSON Parse error: Unrecognized token '\'`.
func SendError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    "mitm_error",
			"code":    status,
		},
	})
	if err != nil {
		body = []byte(`{"error":{"message":"mitm error","type":"mitm_error","code":500}}`)
	}
	_, _ = w.Write(body)
}
