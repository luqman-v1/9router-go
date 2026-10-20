//go:build ignore

package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestMeasureOnceWaitsForResponseBody(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil || !bytes.Contains(data, []byte(`"stream":true`)) {
			t.Errorf("unexpected request: %q %v", data, err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan sample, 1)
	errs := make(chan error, 1)
	go func() {
		got, err := measureOnce(ctx, srv.Client(), srv.URL, []byte(`{"stream":true}`), "synthetic")
		done <- got
		errs <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-done:
		close(release)
		t.Fatal("measurement finished before body was released")
	default:
	}
	close(release)
	got := <-done
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if got.Status != http.StatusOK || !bytes.Contains(got.Body, []byte("[DONE]")) {
		t.Fatalf("incomplete result: %+v", got)
	}
	if got.HeaderLatency > got.FirstBodyByte || got.FirstBodyByte > got.Total {
		t.Fatalf("timings out of order: %+v", got)
	}
}

// TestRunBenchmarkDispatchesEveryRequest pins the remainder case: 7 requests
// over 3 workers used to dispatch only 2*3 = 6, so the reported total quietly
// disagreed with the requested workload.
func TestRunBenchmarkDispatchesEveryRequest(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	res := runBenchmark(Config{Target: srv.URL, APIKey: "synthetic", Requests: 7, Stream: true, Timeout: 5 * time.Second}, 3)
	if got := atomic.LoadInt64(&hits); got != 7 {
		t.Fatalf("server saw %d requests, workload asked for 7", got)
	}
	if res.TotalReqs != 7 || res.SuccessReqs != 7 || res.FailedReqs != 0 || res.SemanticErr != 0 {
		t.Fatalf("unexpected tally: %+v", res)
	}
}

// TestRunBenchmarkRefusesTruncatedStream keeps a cut-short stream out of the
// success count. Counting it as success is how a stalled proxy reports a
// speedup it never earned.
func TestRunBenchmarkRefusesTruncatedStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
	}))
	defer srv.Close()
	res := runBenchmark(Config{Target: srv.URL, APIKey: "synthetic", Requests: 4, Stream: true, Timeout: 5 * time.Second}, 2)
	if res.SuccessReqs != 0 || res.SemanticErr != 4 {
		t.Fatalf("truncated stream counted wrong: %+v", res)
	}
	if res.RPS != 0 {
		t.Fatalf("RPS must be zero when nothing succeeded, got %v", res.RPS)
	}
}
