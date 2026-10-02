package proxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// stallingSSE is an upstream that answers the request and then holds the
// response open without sending anything — the shape a broken chat provider
// takes when it stalls after the answer.
type stallingSSE struct {
	hits    atomic.Int64
	release chan struct{}
}

func newStallingSSE() *stallingSSE {
	return &stallingSSE{release: make(chan struct{})}
}

func (s *stallingSSE) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	s.hits.Add(1)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}
	_, _ = w.Write([]byte("data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n"))
	flusher.Flush()
	<-s.release
}

// TestScanStreamWithDeadlineReleasesAStalledUpstream proves the bound on a
// real response body: the scanner has to stop waiting on an upstream that will
// never send again. The read is otherwise unbounded, so without this the
// request holds its goroutine for as long as the provider feels like.
func TestScanStreamWithDeadlineReleasesAStalledUpstream(t *testing.T) {
	upstream := newStallingSSE()
	srv := httptest.NewServer(upstream)
	defer srv.Close()
	defer close(upstream.release)

	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatalf("get stalled stream: %v", err)
	}
	defer resp.Body.Close()

	pending := atomic.Bool{}
	pending.Store(true)

	done := make(chan error, 1)
	go func() {
		done <- ScanStreamWithDeadline(context.Background(), resp.Body, 50*time.Millisecond,
			pending.Load, func([]byte) {})
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the scan never returned; the stalled upstream holds the caller forever")
	}
}

// TestScanStreamWithDeadlineWaitsWhileNothingIsOwed is the other half of the
// gate. Before a terminal event is owed there is nothing to give up on, and a
// provider that pauses before its first token is slow rather than broken.
func TestScanStreamWithDeadlineWaitsWhileNothingIsOwed(t *testing.T) {
	upstream := newStallingSSE()
	srv := httptest.NewServer(upstream)
	defer srv.Close()
	defer close(upstream.release)

	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatalf("get stalled stream: %v", err)
	}
	defer resp.Body.Close()

	pending := atomic.Bool{} // nothing owed: the bound must not apply

	done := make(chan error, 1)
	go func() {
		done <- ScanStreamWithDeadline(context.Background(), resp.Body, 20*time.Millisecond,
			pending.Load, func([]byte) {})
	}()

	select {
	case <-done:
		t.Fatal("the scan gave up on an upstream that had not owed the client anything")
	case <-time.After(300 * time.Millisecond):
	}
}

// TestScanStreamWithDeadlineResumesAfterEvents checks the window is per event,
// not for the whole stream: a provider that keeps talking is never cut off,
// however long the turn runs.
func TestScanStreamWithDeadlineResumesAfterEvents(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	go func() {
		// One write for the whole stream: the reader's buffer then holds every
		// line at once, so the gaps below land in that buffer rather than on
		// the wire — which is the state a stalled upstream leaves behind.
		var batch bytes.Buffer
		for i := range 4 {
			batch.WriteString("data: {\"n\":" + string(rune('0'+i)) + "}\n\n")
			time.Sleep(60 * time.Millisecond) // longer than the window below
		}
		if _, err := pw.Write(batch.Bytes()); err != nil {
			return
		}
		_ = pw.Close()
	}()

	var seen int
	pending := atomic.Bool{}
	pending.Store(true)
	if err := ScanStreamWithDeadline(context.Background(), pr, 30*time.Millisecond, pending.Load, func([]byte) {
		seen++
	}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if seen != 4 {
		t.Errorf("events seen = %d, want all 4 to survive a gap longer than the window", seen)
	}
}

// TestScanStreamWithDeadlineZeroDisablesTheBound keeps the escape hatch honest:
// a zero timeout leaves the scan exactly as unbounded as ScanStream.
func TestScanStreamWithDeadlineZeroDisablesTheBound(t *testing.T) {
	if err := ScanStreamWithDeadline(context.Background(),
		strings.NewReader("data: {\"a\":1}\n\n"), 0, nil,
		func([]byte) {}); err != nil {
		t.Fatalf("a zero timeout must not break the scan: %v", err)
	}
}

// TestScanStreamWithDeadlineStopsOnContextCancel keeps the bound cancellable
// from the client side: a disconnected request must not wait out the window.
func TestScanStreamWithDeadlineStopsOnContextCancel(t *testing.T) {
	upstream := newStallingSSE()
	srv := httptest.NewServer(upstream)
	defer srv.Close()
	defer close(upstream.release)

	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatalf("get stalled stream: %v", err)
	}
	defer resp.Body.Close()

	ctx, cancel := context.WithCancel(context.Background())
	pending := atomic.Bool{}
	pending.Store(true)

	done := make(chan error, 1)
	go func() {
		done <- ScanStreamWithDeadline(ctx, resp.Body, time.Hour, pending.Load, func([]byte) {})
	}()
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled context must release the scan without waiting out the bound")
	}
}