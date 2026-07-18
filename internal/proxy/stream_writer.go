package proxy

import (
	"fmt"
	"io"
	"net/http"
)

// StreamWriter wraps an io.Writer and handles flushing if the writer supports it.
type StreamWriter struct {
	w       io.Writer
	flusher http.Flusher
}

// NewStreamWriter creates a new StreamWriter.
func NewStreamWriter(w io.Writer) *StreamWriter {
	flusher, _ := w.(http.Flusher)
	return &StreamWriter{
		w:       w,
		flusher: flusher,
	}
}

// WriteChunk writes a formatted SSE data chunk: "data: <data>\n\n"
// and flushes the writer if it supports flushing.
func (sw *StreamWriter) WriteChunk(data []byte) (int, error) {
	n, err := fmt.Fprintf(sw.w, "data: %s\n\n", data)
	if err != nil {
		return n, err
	}
	if sw.flusher != nil {
		sw.flusher.Flush()
	}
	return n, nil
}

// WriteChunk is a standalone function that writes an SSE chunk to w and flushes if w is an http.Flusher.
func WriteChunk(w io.Writer, data []byte) (int, error) {
	n, err := fmt.Fprintf(w, "data: %s\n\n", data)
	if err != nil {
		return n, err
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return n, nil
}
