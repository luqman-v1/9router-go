package proxy

import (
	"io"
	"net/http"
)

// WriteSSEHeaders sets standard SSE headers on the response and writes HTTP 200.
// Returns the http.Flusher if the ResponseWriter supports it.
func WriteSSEHeaders(w http.ResponseWriter) http.Flusher {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	f, _ := w.(http.Flusher)
	return f
}

// SSECopy reads from upstream in a raw loop and writes each chunk to the client.
// A simplified passthrough that does NOT parse SSE framing — use when translation is not needed.
func SSECopy(w http.ResponseWriter, upstream io.Reader, flusher http.Flusher) error {
	buf := make([]byte, 4096)
	for {
		n, err := upstream.Read(buf)
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
