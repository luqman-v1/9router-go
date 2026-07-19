package handlers

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"time"
)

const proxyBase = "http://localhost:20128"

// FetchRouter sends a request body to the 9router proxy endpoint and returns the response.
func FetchRouter(body []byte, endpoint string, headers http.Header, apiKey string) (*http.Response, error) {
	url := proxyBase + endpoint
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	// Forward original auth headers
	if auth := headers.Get("Authorization"); auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if ct := headers.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
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
func SendError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write([]byte(fmt.Sprintf(`{"error":{"message":"%s","type":"mitm_error","code":%d}}`, msg, status)))
}
