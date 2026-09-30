package proxy

import (
	"context"
	"errors"
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

	shutdown.TestReset()
	defer shutdown.TestReset()
	shutdown.Cancel()
	_, err := r.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("expected Read to error after shutdown")
	}
}

func TestStallReaderAbortsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pr, pw := io.Pipe()
	defer pw.Close()

	r := NewStallReaderWithContext(ctx, pr, time.Minute, "test-cancel")
	defer r.Close()

	cancel()
	time.Sleep(20 * time.Millisecond)

	_, err := r.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("expected Read to error after context cancel")
	}
}

// TestStallReaderReportsTimeout covers issue #57 end to end: a provider that
// goes silent mid-answer must reach the client as a 504 timeout, distinguishable
// from a socket that merely died. The watchdog only records a flag; if Read did
// not translate it, the client would be told "connection lost" instead.
func TestStallReaderReportsTimeout(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()

	r := NewStallReader(pr, 20*time.Millisecond, "stall-e2e")
	defer r.Close()

	go func() {
		_, _ = pw.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"))
	}()

	rec := &mockResponseWriter{}
	err := SSECopy(rec, r, rec, nil)
	if err == nil {
		t.Fatal("expected the stalled stream to report an error")
	}
	if !errors.Is(err, ErrStreamStall) {
		t.Errorf("error = %v, want it to wrap ErrStreamStall", err)
	}

	frame, ok := errorFrame(t, rec.String())
	if !ok {
		t.Fatalf("client got no error frame: %q", rec.String())
	}
	if frame.Error.Code != "gateway_timeout" {
		t.Errorf("code = %q, want gateway_timeout", frame.Error.Code)
	}
}
