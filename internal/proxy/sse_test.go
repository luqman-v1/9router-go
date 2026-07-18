package proxy

import (
	"bytes"
	"errors"
	"testing"
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
