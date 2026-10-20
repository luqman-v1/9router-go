//go:build ignore

package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// TestLoadPayloadFileRejectsNonObject pins that a payload file holding
// something other than a JSON object is refused. The benchmark sends the file
// verbatim as an HTTP body, and a body the gateway cannot parse measures
// nothing.
func TestLoadPayloadFileRejectsNonObject(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name    string
		content string
		wantErr bool
	}{
		{"object", `{"model":"gpt-4o"}`, false},
		{"array", `["gpt-4o"]`, true},
		{"scalar", `"gpt-4o"`, true},
		{"malformed", `{`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name+".json")
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			body, err := loadPayloadFile(path)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
			if !tc.wantErr && string(body) != tc.content {
				t.Fatalf("body=%q want=%q", body, tc.content)
			}
		})
	}
}

// TestLoadPayloadFileMissingFile keeps a missing file a startup error rather
// than a silent fallback to the built-in payload: measuring a different
// request than the operator asked for is worse than measuring nothing.
func TestLoadPayloadFileMissingFile(t *testing.T) {
	if _, err := loadPayloadFile(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Fatal("missing payload file was accepted")
	}
}

// TestRunBenchmarkSendsPayloadFileBytes pins that a payload file reaches the
// server byte for byte, so the matrix really drives the request shape it names.
func TestRunBenchmarkSendsPayloadFileBytes(t *testing.T) {
	want := `{"model":"bench/gpt-4o","stream":true,"tools":[{"type":"function"}]}`
	var seen atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		seen.Store(string(data))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	res := runBenchmark(Config{
		Target: srv.URL, APIKey: "synthetic", Requests: 3, Stream: true,
		Timeout: 5 * time.Second, Payload: []byte(want),
	}, 2)
	if res.SuccessReqs != 3 {
		t.Fatalf("tally: %+v", res)
	}
	if got, _ := seen.Load().(string); got != want {
		t.Fatalf("server saw %q want %q", got, want)
	}
}
