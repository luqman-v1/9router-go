package chat

import (
	"net/http"
	"sync/atomic"
)

// TODO: This should be used in fallback.go to wrap the ResponseWriter.
// Example: cw := newCommittedResponseWriter(w)
// Then check cw.IsCommitted() before retrying.

// committedResponseWriter wraps http.ResponseWriter to track whether
// response headers have been sent to the client (i.e., WriteHeader or
// first Write has been called). This is used by fallback logic to
// determine if a retry is safe.
type committedResponseWriter struct {
	http.ResponseWriter
	committed int32 // atomic: 1 if headers have been sent
}

func newCommittedResponseWriter(w http.ResponseWriter) *committedResponseWriter {
	return &committedResponseWriter{ResponseWriter: w}
}

func (cw *committedResponseWriter) WriteHeader(code int) {
	atomic.StoreInt32(&cw.committed, 1)
	cw.ResponseWriter.WriteHeader(code)
}

func (cw *committedResponseWriter) Write(b []byte) (int, error) {
	atomic.StoreInt32(&cw.committed, 1)
	return cw.ResponseWriter.Write(b)
}

// IsCommitted returns true if response headers have been sent.
func (cw *committedResponseWriter) IsCommitted() bool {
	return atomic.LoadInt32(&cw.committed) == 1
}

// Flush implements http.Flusher if the underlying writer supports it.
func (cw *committedResponseWriter) Flush() {
	if f, ok := cw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
