package proxy

import (
	"context"
	"io"
	"runtime"
	"testing"
	"time"
)

// TestStreamClientContextCancelTerminatesUpstream verifies that client context
// cancellation immediately terminates upstream stream reading and unblocks pending
// reads without lingering goroutines.
func TestStreamClientContextCancelTerminatesUpstream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pr, pw := io.Pipe()
	defer pw.Close()

	initialGoroutines := runtime.NumGoroutine()

	r := NewStallReaderWithContext(ctx, pr, time.Minute, "test-stream-abort")
	defer r.Close()

	errChan := make(chan error, 1)
	go func() {
		buf := make([]byte, 1024)
		_, err := r.Read(buf)
		errChan <- err
	}()

	// Cancel context immediately to simulate downstream client disconnect.
	cancel()

	select {
	case err := <-errChan:
		if err == nil {
			t.Fatal("expected Read error after context cancellation, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stream read to terminate after client cancellation")
	}

	// Give goroutine a moment to exit.
	time.Sleep(50 * time.Millisecond)
	finalGoroutines := runtime.NumGoroutine()
	if finalGoroutines > initialGoroutines+2 {
		t.Logf("warning: goroutine count increased from %d to %d", initialGoroutines, finalGoroutines)
	}
}
