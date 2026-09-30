package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/iotest"
	"sync"
	"time"
)

func TestScanStreamChunks(t *testing.T) {
	streamData := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n")
	buf := bytes.NewBuffer(streamData)

	var chunks [][]byte
	err := ScanStream(buf, func(chunk []byte) {
		chunks = append(chunks, chunk)
	})

	if err != nil {
		t.Fatalf("ScanStream failed: %v", err)
	}

	if len(chunks) != 2 {
		t.Errorf("expected 2 chunks, got %d", len(chunks))
	}
	if !bytes.Equal(chunks[1], []byte("[DONE]")) {
		t.Errorf("expected last chunk to be [DONE], got %s", string(chunks[1]))
	}
}

func TestScanStreamAccumulatesEvents(t *testing.T) {
	// Multi-line data payload, keep-alive, comment, and CRLF endings.
	streamData := []byte(": keep-alive\r\n" +
		"data: {\"a\":1,\r\n" +
		"data: \"b\":2}\r\n" +
		"\r\n" +
		"data: \r\n" +
		"\r\n" +
		"data: [DONE]\r\n")
	buf := bytes.NewBuffer(streamData)

	var chunks [][]byte
	err := ScanStream(buf, func(chunk []byte) {
		chunks = append(chunks, chunk)
	})

	if err != nil {
		t.Fatalf("ScanStream failed: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d: %q", len(chunks), chunks)
	}
	// Multi-line payload is joined with a newline.
	want := "{\"a\":1,\n\"b\":2}"
	if string(chunks[0]) != want {
		t.Errorf("expected %q, got %q", want, string(chunks[0]))
	}
	if string(chunks[1]) != "[DONE]" {
		t.Errorf("expected last chunk [DONE], got %q", string(chunks[1]))
	}
}

type mockFlusher struct {
	bytes.Buffer
	flushed bool
}

func (f *mockFlusher) Flush() {
	f.flushed = true
}

func TestStreamWriterAndWriteChunk(t *testing.T) {
	// Test StreamWriter with a flusher
	flusher := &mockFlusher{}
	sw := NewStreamWriter(flusher)

	n, err := sw.WriteChunk([]byte("hello"))
	if err != nil {
		t.Fatalf("WriteChunk failed: %v", err)
	}
	expected := "data: hello\n\n"
	if flusher.String() != expected {
		t.Errorf("expected output %q, got %q", expected, flusher.String())
	}
	if n != len(expected) {
		t.Errorf("expected length %d, got %d", len(expected), n)
	}
	if !flusher.flushed {
		t.Errorf("expected flusher to be called")
	}

	// Test WriteChunk directly
	flusher2 := &mockFlusher{}
	n2, err2 := WriteChunk(flusher2, []byte("world"))
	if err2 != nil {
		t.Fatalf("WriteChunk failed: %v", err2)
	}
	expected2 := "data: world\n\n"
	if flusher2.String() != expected2 {
		t.Errorf("expected output %q, got %q", expected2, flusher2.String())
	}
	if n2 != len(expected2) {
		t.Errorf("expected length %d, got %d", len(expected2), n2)
	}
	if !flusher2.flushed {
		t.Errorf("expected flusher to be called")
	}
}

type errorWriter struct{}

func (ew *errorWriter) Write(p []byte) (n int, err error) {
	return 0, errors.New("write error")
}

func TestStreamWriterErrors(t *testing.T) {
	ew := &errorWriter{}
	sw := NewStreamWriter(ew)

	_, err := sw.WriteChunk([]byte("hello"))
	if err == nil {
		t.Error("expected error, got nil")
	}

	_, err2 := WriteChunk(ew, []byte("hello"))
	if err2 == nil {
		t.Error("expected error, got nil")
	}
}

// mockResponseWriter is written from the heartbeat goroutine while the test
// reads it, so every field is behind a mutex. The embedded buffer cannot be
// locked on its own, so writes are serialized through w and String reads
// through a snapshot.
type mockResponseWriter struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	header  http.Header
	code    int
	flushed bool
}

// String snapshots the buffer under the lock, so a test never reads a buffer
// the heartbeat goroutine is appending to.
func (m *mockResponseWriter) String() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.buf.String()
}

func (m *mockResponseWriter) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.buf.Write(p)
}

func (m *mockResponseWriter) Header() http.Header {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.header == nil {
		m.header = make(http.Header)
	}
	return m.header
}

func (m *mockResponseWriter) WriteHeader(code int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.code = code
}

func (m *mockResponseWriter) Flush() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.flushed = true
}

// wasFlushed reports whether Flush was called at least once.
func (m *mockResponseWriter) wasFlushed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.flushed
}


func TestHeartbeatWriter_EmitsKeepAliveWhenIdle(t *testing.T) {
	rec := &mockResponseWriter{}
	hw := NewHeartbeatWriter(context.Background(), rec, 25*time.Millisecond)
	defer hw.Close()

	// Wait for 2 heartbeat ticks (idle)
	time.Sleep(70 * time.Millisecond)

	out := rec.String()
	if !strings.Contains(out, ": keep-alive\n\n") {
		t.Errorf("expected keep-alive in output, got %q", out)
	}
	if !rec.wasFlushed() {
		t.Errorf("expected flusher to be called")
	}
}

func TestHeartbeatWriter_ActiveStreamDelaysKeepAlive(t *testing.T) {
	rec := &mockResponseWriter{}
	hw := NewHeartbeatWriter(context.Background(), rec, 50*time.Millisecond)
	defer hw.Close()
	for range 4 {
		time.Sleep(15 * time.Millisecond)
		hw.Write([]byte("data: chunk\n\n"))
	}

	out := rec.String()
	if strings.Contains(out, ": keep-alive\n\n") {
		t.Errorf("did not expect keep-alive while stream was active, got %q", out)
	}
	if !strings.Contains(out, "data: chunk\n\n") {
		t.Errorf("expected data chunks, got %q", out)
	}
}

func TestHeartbeatWriter_ClosesOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	rec := &mockResponseWriter{}
	hw := NewHeartbeatWriter(ctx, rec, 20*time.Millisecond)

	cancel()
	time.Sleep(50 * time.Millisecond)

	// Writing after close should return ErrClosedPipe
	_, err := hw.Write([]byte("after close"))
	if err == nil {
		t.Errorf("expected write error on closed writer, got nil")
	}
}

func TestSSECopy_SynthesizesTerminalOnAbruptClose(t *testing.T) {
	t.Run("synthesizes network_error and DONE when stream ends without finish_reason", func(t *testing.T) {
		rec := &mockResponseWriter{}
		// Stream delivers content but EOF arrives before finish_reason or [DONE]
		rawStream := bytes.NewReader([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"))

		err := SSECopy(rec, rawStream, rec, nil)
		if err != nil {
			t.Fatalf("SSECopy failed: %v", err)
		}

		out := rec.String()
		if !strings.Contains(out, "\"finish_reason\":\"network_error\"") {
			t.Errorf("expected synthesized network_error finish_reason, got %q", out)
		}
		if !strings.Contains(out, "data: [DONE]\n\n") {
			t.Errorf("expected data: [DONE], got %q", out)
		}
	})

	t.Run("preserves native finish_reason and DONE without duplicate terminal", func(t *testing.T) {
		rec := &mockResponseWriter{}
		normalStream := bytes.NewReader([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))

		err := SSECopy(rec, normalStream, rec, nil)
		if err != nil {
			t.Fatalf("SSECopy failed: %v", err)
		}

		out := rec.String()
		if strings.Contains(out, "network_error") {
			t.Errorf("normal stream should not gain network_error, got %q", out)
		}
		// Should only have one [DONE]
		if strings.Count(out, "[DONE]") != 1 {
			t.Errorf("expected exactly 1 [DONE], got %d in %q", strings.Count(out, "[DONE]"), out)
		}
	})

	t.Run("separates un-terminated tail from DONE cleanly", func(t *testing.T) {
		rec := &mockResponseWriter{}
		// Stream ends mid-line without trailing \n\n
		unterminated := bytes.NewReader([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"tail\"}}]}"))

		err := SSECopy(rec, unterminated, rec, nil)
		if err != nil {
			t.Fatalf("SSECopy failed: %v", err)
		}

		out := rec.String()
		// Must not collapse tail with [DONE]
		if strings.Contains(out, "}}data: [DONE]") || strings.Contains(out, "}}\ndata: [DONE]") {
			t.Errorf("expected blank line separation before [DONE], got %q", out)
		}
		if !strings.HasSuffix(out, "data: [DONE]\n\n") {
			t.Errorf("expected stream to end with 'data: [DONE]\\n\\n', got %q", out)
		}
	})

	t.Run("recognizes Claude stop_reason and message_stop without synthesizing network_error", func(t *testing.T) {
		rec := &mockResponseWriter{}
		claudeStream := bytes.NewReader([]byte("event: message_start\ndata: {\"type\":\"message_start\"}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))

		err := SSECopy(rec, claudeStream, rec, nil)
		if err != nil {
			t.Fatalf("SSECopy failed: %v", err)
		}

		out := rec.String()
		if strings.Contains(out, "network_error") {
			t.Errorf("Claude stream with stop_reason should not gain network_error, got %q", out)
		}
		if !strings.Contains(out, "data: [DONE]\n\n") {
			t.Errorf("expected data: [DONE], got %q", out)
		}
	})
}

// TestSSECopy_InjectsStopBeforeBareDone covers the Oh My Pi failure
// "OpenAI completions stream closed before a finish_reason was received": a
// non-compliant upstream streams content deltas and then emits [DONE] with no
// terminal finish_reason chunk.
func TestSSECopy_InjectsStopBeforeBareDone(t *testing.T) {
	tests := []struct {
		name   string
		stream string
	}{
		{
			name:   "deltas then DONE without finish_reason",
			stream: "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n",
		},
		{
			name:   "null finish_reason chunks then DONE",
			stream: "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n",
		},
		{
			name:   "DONE without trailing newline at EOF",
			stream: "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &mockResponseWriter{}
			if err := SSECopy(rec, strings.NewReader(tt.stream), rec, nil); err != nil {
				t.Fatalf("SSECopy failed: %v", err)
			}
			out := rec.String()
			if !strings.Contains(out, `"finish_reason":"stop"`) {
				t.Fatalf("expected injected stop terminal, got %q", out)
			}
			if strings.Contains(out, "network_error") {
				t.Errorf("deliberate DONE must complete with stop, not network_error, got %q", out)
			}
			stopIdx := strings.Index(out, `"finish_reason":"stop"`)
			doneIdx := strings.Index(out, "data: [DONE]")
			if doneIdx < 0 {
				t.Fatalf("expected [DONE] sentinel, got %q", out)
			}
			if stopIdx > doneIdx {
				t.Errorf("terminal must precede [DONE], got %q", out)
			}
			if strings.Count(out, "[DONE]") != 1 {
				t.Errorf("expected exactly 1 [DONE], got %d in %q", strings.Count(out, "[DONE]"), out)
			}
		})
	}

	t.Run("sentinel split across reads still gains terminal", func(t *testing.T) {
		rec := &mockResponseWriter{}
		raw := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"
		if err := SSECopy(rec, iotest.OneByteReader(strings.NewReader(raw)), rec, nil); err != nil {
			t.Fatalf("SSECopy failed: %v", err)
		}
		out := rec.String()
		if !strings.Contains(out, `"finish_reason":"stop"`) {
			t.Errorf("expected injected stop terminal on split reads, got %q", out)
		}
		if strings.Count(out, "[DONE]") != 1 {
			t.Errorf("expected exactly 1 [DONE], got %d in %q", strings.Count(out, "[DONE]"), out)
		}
	})

	t.Run("[DONE] mentioned inside content does not end the stream", func(t *testing.T) {
		rec := &mockResponseWriter{}
		raw := "data: {\"choices\":[{\"delta\":{\"content\":\"say [DONE] now\"}}]}\n\n"
		if err := SSECopy(rec, strings.NewReader(raw), rec, nil); err != nil {
			t.Fatalf("SSECopy failed: %v", err)
		}
		out := rec.String()
		if !strings.Contains(out, "say [DONE] now") {
			t.Errorf("content must pass through untouched, got %q", out)
		}
		// No real sentinel arrived, so EOF synthesis (network_error + DONE)
		// must still close the stream.
		if !strings.Contains(out, `"finish_reason":"network_error"`) {
			t.Errorf("expected EOF network_error synthesis, got %q", out)
		}
		if !strings.HasSuffix(out, "data: [DONE]\n\n") {
			t.Errorf("expected stream to end with [DONE], got %q", out)
		}
	})

	t.Run("read error closes the stream with an in-band error frame, not a finish_reason", func(t *testing.T) {
		rec := &mockResponseWriter{}
		upstream := io.MultiReader(
			strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"),
			iotest.ErrReader(errors.New("connection reset by peer")),
		)
		if err := SSECopy(rec, upstream, rec, nil); err == nil {
			t.Fatal("expected the upstream read error to be reported")
		}
		out := rec.String()
		// A client must be able to tell a dropped stream from a finished
		// answer. A finish_reason says "stop"/"network_error", which every
		// OpenAI client reads as a normal completion.
		if !strings.Contains(out, `{"error":{"message":"upstream connection lost"`) {
			t.Errorf("expected an in-band error frame, got %q", out)
		}
		if strings.Contains(out, "finish_reason") {
			t.Errorf("abort must not fabricate a finish_reason, got %q", out)
		}
		if !strings.HasSuffix(out, "data: [DONE]\n\n") {
			t.Errorf("expected the stream to close with [DONE], got %q", out)
		}
	})
}

// TestSSECopy_KeepsSingleTerminalOnCompliantStream pins that a stream the
// upstream already terminated correctly is relayed untouched. sseHasNonNullValue
// searched for `"finish_reason":` — two quotes before the colon, which no JSON
// contains — so hasTerminal was always false and every compliant stream was
// given a second terminal before its [DONE].
func TestSSECopy_KeepsSingleTerminalOnCompliantStream(t *testing.T) {
	tests := []struct {
		name   string
		stream string
	}{
		{
			// Real OpenAI frames carry id/object/created, which is what tells
			// the upstream's own terminal apart from an injected one.
			name: "OpenAI stream already closed with stop",
			stream: "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n" +
				"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
				"data: [DONE]\n\n",
		},
		{
			name: "OpenAI stream already closed with tool_calls",
			stream: "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n" +
				"data: {\"choices\":[{\"index\":0,\"finish_reason\":\"tool_calls\",\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"type\":\"function\",\"function\":{\"name\":\"f\",\"arguments\":\"{}\"}}]}}]}\n\n" +
				"data: [DONE]\n\n",
		},
		{
			name: "Claude stream already closed with end_turn",
			stream: "event: message_start\ndata: {\"type\":\"message_start\"}\n\n" +
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n" +
				"data: [DONE]\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &mockResponseWriter{}
			if err := SSECopy(rec, strings.NewReader(tt.stream), rec, nil); err != nil {
				t.Fatalf("SSECopy failed: %v", err)
			}
			out := rec.String()
			if got := strings.Count(out, sseStopTerminal); got != 0 {
				t.Errorf("expected no injected stop terminal on an already-closed stream, got %d in %q", got, out)
			}
			if got := strings.Count(out, sseTruncatedTerminal); got != 0 {
				t.Errorf("a complete stream must not be reported as truncated, got %q", out)
			}
			if got := strings.Count(out, "data: [DONE]"); got != 1 {
				t.Errorf("expected exactly 1 [DONE], got %d in %q", got, out)
			}
		})
	}
}

// TestSSECopy_QuotedTerminalInsideContentIsNotATerminal pins the key-position
// requirement: content that quotes the field back is escaped JSON text, and
// reading it as a terminal would leave the real stream unterminated.
func TestSSECopy_QuotedTerminalInsideContentIsNotATerminal(t *testing.T) {
	rec := &mockResponseWriter{}
	stream := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"field: \\\"finish_reason\\\": \\\"stop\\\"\"},\"finish_reason\":null}]}\n\n" +
		"data: [DONE]\n\n"

	if err := SSECopy(rec, strings.NewReader(stream), rec, nil); err != nil {
		t.Fatalf("SSECopy failed: %v", err)
	}
	out := rec.String()
	if !strings.Contains(out, "field:") {
		t.Fatalf("content did not survive, got %q", out)
	}
	if got := strings.Count(out, sseStopTerminal); got != 1 {
		t.Errorf("expected exactly 1 injected terminal, got %d in %q", got, out)
	}
}
