package proxy

import (
	"io"
	"testing"
	"time"

	"9router/proxy/internal/shutdown"
)

// TestStallReaderAbortsOnShutdown verifies that starting shutdown closes the
// underlying reader, unblocking a pending Read so in-flight streams end fast.
func TestStallReaderAbortsOnShutdown(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()

	r := NewStallReader(pr, time.Minute, "test")
	defer r.Close()

	shutdown.Cancel()
	_, err := r.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("expected Read to error after shutdown")
	}
}
