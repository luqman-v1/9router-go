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

// TestClassifyEvent pins the semantic-event map for all three wire shapes the
// gateway serves. Metadata, role-only deltas, empty payloads, terminals and
// usage trailers must read as evNone: timing one of those as the first token
// would report a TTFT the client never received.
func TestClassifyEvent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
		want    eventKind
	}{
		// metadata / keep-alive / terminal
		{"done", `[DONE]`, evNone},
		{"empty", ``, evNone},
		{"role-only", `{"choices":[{"delta":{"role":"assistant","content":""}}]}`, evNone},
		{"finish", `{"choices":[{"delta":{},"finish_reason":"stop"}]}`, evNone},
		{"usage-trailer", `{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2}}`, evNone},
		{"message-start", `{"type":"message_start","message":{"usage":{"input_tokens":10}}}`, evNone},
		{"message-delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`, evNone},
		{"message-stop", `{"type":"message_stop"}`, evNone},
		{"ping", `{"type":"ping"}`, evNone},
		{"responses-created", `{"type":"response.created","response":{"status":"in_progress"}}`, evNone},
		{"responses-completed", `{"type":"response.completed","response":{"status":"completed"}}`, evNone},
		{"content-block-start-text", `{"type":"content_block_start","content_block":{"type":"text","text":""}}`, evNone},
		{"garbage", `not json`, evNone},

		// text
		{"chat-text", `{"choices":[{"delta":{"content":"Hello"}}]}`, evText},
		{"chat-text-empty-string", `{"choices":[{"delta":{"content":""}}]}`, evNone},
		{"chat-legacy-text", `{"choices":[{"text":"Hello"}]}`, evText},
		{"claude-text", `{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello"}}`, evText},
		{"claude-text-empty", `{"type":"content_block_delta","delta":{"type":"text_delta","text":""}}`, evNone},
		{"responses-text", `{"type":"response.output_text.delta","delta":"Hello"}`, evText},

		// tool-only
		{"chat-tool", `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1"}]}}]}`, evTool},
		{"chat-function-call", `{"choices":[{"delta":{"function_call":{"name":"f"}}}]}`, evTool},
		{"claude-tool-delta", `{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{}"}}`, evTool},
		{"claude-tool-start", `{"type":"content_block_start","content_block":{"type":"tool_use","name":"f"}}`, evTool},
		{"responses-tool", `{"type":"response.function_call_arguments.delta","delta":"{}"}`, evTool},

		// reasoning-only
		{"chat-reasoning-content", `{"choices":[{"delta":{"reasoning_content":"thinking"}}]}`, evReasoning},
		{"chat-reasoning", `{"choices":[{"delta":{"reasoning":"thinking"}}]}`, evReasoning},
		{"claude-thinking", `{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"hmm"}}`, evReasoning},
		{"claude-thinking-start", `{"type":"content_block_start","content_block":{"type":"thinking"}}`, evReasoning},
		{"responses-reasoning", `{"type":"response.reasoning_summary_text.delta","delta":"hmm"}`, evReasoning},

		// typed error
		{"error-event", `{"type":"error","error":{"message":"overloaded"}}`, evError},
		{"error-envelope", `{"error":{"message":"overloaded"}}`, evError},
		{"responses-failed", `{"type":"response.failed","response":{"status":"failed"}}`, evError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyEvent([]byte(tc.payload)); got != tc.want {
				t.Fatalf("classifyEvent(%s) = %v want %v", tc.payload, got, tc.want)
			}
		})
	}
}

// ttftServer streams the given frames with an optional pause, so a test can
// prove TTFT lands on the semantic event rather than on the first byte.
func ttftServer(t *testing.T, pause time.Duration, frames ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for i, frame := range frames {
			if i > 0 && pause > 0 {
				time.Sleep(pause)
			}
			_, _ = io.WriteString(w, "data: "+frame+"\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func measureTTFT(t *testing.T, srv *httptest.Server) sample {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := measureOnce(ctx, srv.Client(), srv.URL, []byte(`{"stream":true}`), "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// TestMeasureTTFTSkipsMetadataBeforeText is the case that makes TTFT a
// different number from first body byte: the stream opens with metadata, a
// keep-alive and a role-only delta, then pauses before the first token. TTFT
// must land after the pause.
func TestMeasureTTFTSkipsMetadataBeforeText(t *testing.T) {
	const pause = 40 * time.Millisecond
	srv := ttftServer(t, pause,
		`{"type":"message_start","message":{"usage":{"input_tokens":10}}}`,
		"",
		`{"choices":[{"delta":{"role":"assistant","content":""}}]}`,
		`{"choices":[{"delta":{"content":"Hello"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`[DONE]`,
	)
	got := measureTTFT(t, srv)
	if got.TTFT == 0 {
		t.Fatal("no TTFT recorded for a stream that carried text")
	}
	if got.TTFT < pause {
		t.Fatalf("TTFT %v landed before the token pause %v — metadata was timed", got.TTFT, pause)
	}
	if got.TTFT > got.Total {
		t.Fatalf("TTFT %v after total %v", got.TTFT, got.Total)
	}
	if got.TTFT <= got.FirstBodyByte {
		t.Fatalf("TTFT %v not distinguishable from first body byte %v", got.TTFT, got.FirstBodyByte)
	}
}

// TestMeasureTTFTCountsToolOnly keeps a tool-only turn legitimately fast: a
// stream that never emits text still produced an answer the client can act on.
func TestMeasureTTFTCountsToolOnly(t *testing.T) {
	srv := ttftServer(t, 0,
		`{"type":"message_start","message":{"usage":{"input_tokens":10}}}`,
		`{"type":"content_block_start","content_block":{"type":"tool_use","id":"toolu_1","name":"mock_tool"}}`,
		`{"type":"message_stop"}`,
	)
	if got := measureTTFT(t, srv); got.TTFT == 0 {
		t.Fatalf("tool-only stream produced no TTFT: %+v", got)
	}
}

// TestMeasureTTFTCountsReasoningOnly is the same for a reasoning turn.
func TestMeasureTTFTCountsReasoningOnly(t *testing.T) {
	srv := ttftServer(t, 0,
		`{"choices":[{"delta":{"reasoning_content":"thinking"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`[DONE]`,
	)
	if got := measureTTFT(t, srv); got.TTFT == 0 {
		t.Fatalf("reasoning-only stream produced no TTFT: %+v", got)
	}
}

// TestMeasureTTFTRejectsEventOnlyStream pins that metadata plus a terminal is
// not a token. Reporting a TTFT here would name a first token that never came.
func TestMeasureTTFTRejectsEventOnlyStream(t *testing.T) {
	srv := ttftServer(t, 0,
		`{"type":"response.created","response":{"status":"in_progress"}}`,
		`{"type":"response.completed","response":{"status":"completed","usage":{"output_tokens":0}}}`,
	)
	got := measureTTFT(t, srv)
	if got.TTFT != 0 {
		t.Fatalf("event-only stream reported TTFT %v", got.TTFT)
	}
	if !completeBody(true, got.Body) {
		t.Fatal("event-only stream should still count as complete")
	}
}

// TestMeasureTTFTRejectsTypedError keeps a typed error event from being read
// as a first token. The stream ends without an answer, and a TTFT for it would
// be latency to a failure.
func TestMeasureTTFTRejectsTypedError(t *testing.T) {
	srv := ttftServer(t, 0,
		`{"type":"error","error":{"message":"model overloaded"}}`,
	)
	got := measureTTFT(t, srv)
	if got.TTFT != 0 {
		t.Fatalf("typed error reported TTFT %v", got.TTFT)
	}
}

// TestMeasureTTFTIgnoresUsageTrailer pins that a trailer after the terminal
// does not move the numbers: it arrives after the answer, and TTFT is about
// the first one.
func TestMeasureTTFTIgnoresUsageTrailer(t *testing.T) {
	srv := ttftServer(t, 0,
		`{"choices":[{"delta":{"content":"Hi"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`,
		`[DONE]`,
	)
	got := measureTTFT(t, srv)
	if got.TTFT == 0 {
		t.Fatal("no TTFT recorded")
	}
	if !completeBody(true, got.Body) {
		t.Fatal("terminal missing")
	}
	if got.TTFT > got.Total {
		t.Fatalf("TTFT %v after total %v", got.TTFT, got.Total)
	}
}

// TestRunBenchmarkReportsTTFT pins that the aggregate carries TTFT and says
// how many samples it came from, so a level with no semantic events shows up
// as empty rather than as a zero-millisecond first token.
func TestRunBenchmarkReportsTTFT(t *testing.T) {
	srv := ttftServer(t, 0,
		`{"choices":[{"delta":{"content":"Hi"}}]}`,
		`[DONE]`,
	)
	res := runBenchmark(Config{Target: srv.URL, APIKey: "synthetic", Requests: 4, Stream: true, Timeout: 5 * time.Second}, 2)
	if res.TTFTSamples != 4 || res.TTFTAvg <= 0 || res.TTFTP50 <= 0 || res.TTFTMax <= 0 {
		t.Fatalf("TTFT missing from the aggregate: %+v", res)
	}
	if res.TTFTP50 > res.TTFTMax || res.TTFTP95 > res.TTFTMax {
		t.Fatalf("TTFT percentiles out of order: %+v", res)
	}
}

// TestRunBenchmarkTTFTEmptyWhenNoSemanticEvent pins the empty case end to end.
func TestRunBenchmarkTTFTEmptyWhenNoSemanticEvent(t *testing.T) {
	srv := ttftServer(t, 0,
		`{"type":"response.created","response":{"status":"in_progress"}}`,
		`{"type":"response.completed","response":{"status":"completed"}}`,
	)
	res := runBenchmark(Config{Target: srv.URL, APIKey: "synthetic", Requests: 3, Stream: true, Timeout: 5 * time.Second}, 2)
	if res.SuccessReqs != 3 {
		t.Fatalf("tally: %+v", res)
	}
	if res.TTFTSamples != 0 || res.TTFTAvg != 0 {
		t.Fatalf("event-only responses produced TTFT: %+v", res)
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
