package proxy

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewStreamWriter_NonFlusher(t *testing.T) {
	var buf bytes.Buffer
	sw := NewStreamWriter(&buf)
	if sw.w == nil {
		t.Error("expected writer to be set")
	}
	if sw.flusher != nil {
		t.Error("expected nil flusher for a non-Flusher writer")
	}
}

func TestNewStreamWriter_Flusher(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := NewStreamWriter(rec)
	if sw.flusher == nil {
		t.Error("expected non-nil flusher for httptest.Recorder (implements http.Flusher)")
	}
}

func TestStreamWriter_WriteChunk(t *testing.T) {
	var buf bytes.Buffer
	sw := NewStreamWriter(&buf)

	n, err := sw.WriteChunk([]byte(`{"hello":"world"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "data: {\"hello\":\"world\"}\n\n"
	if buf.String() != want {
		t.Errorf("expected %q, got %q", want, buf.String())
	}
	if n != len(want) {
		t.Errorf("expected written %d bytes, got %d", len(want), n)
	}
}

func TestStreamWriter_WriteChunk_Multiple(t *testing.T) {
	var buf bytes.Buffer
	sw := NewStreamWriter(&buf)

	if _, err := sw.WriteChunk([]byte("a")); err != nil {
		t.Fatalf("first chunk error: %v", err)
	}
	if _, err := sw.WriteChunk([]byte("b")); err != nil {
		t.Fatalf("second chunk error: %v", err)
	}
	want := "data: a\n\ndata: b\n\n"
	if buf.String() != want {
		t.Errorf("expected %q, got %q", want, buf.String())
	}
}

func TestStreamWriter_WriteChunk_FlusherDoesNotPanic(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := NewStreamWriter(rec)

	if _, err := sw.WriteChunk([]byte("x")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "data: x") {
		t.Errorf("expected recorder to contain chunk, got %q", rec.Body.String())
	}
}

func TestWriteChunk_Standalone(t *testing.T) {
	var buf bytes.Buffer
	n, err := WriteChunk(&buf, []byte("payload"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "data: payload\n\n"
	if buf.String() != want {
		t.Errorf("expected %q, got %q", want, buf.String())
	}
	if n != len(want) {
		t.Errorf("expected %d bytes, got %d", len(want), n)
	}
}

func TestWriteChunk_StandaloneWithFlusher(t *testing.T) {
	rec := httptest.NewRecorder()
	if _, err := WriteChunk(rec, []byte("y")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "data: y") {
		t.Errorf("expected chunk written to flusher recorder, got %q", rec.Body.String())
	}
}

func TestWriteChunk_PropagatesWriteError(t *testing.T) {
	fw := &failWriter{}
	_, err := WriteChunk(fw, []byte("z"))
	if err == nil {
		t.Fatal("expected error from failing writer")
	}
}

// failWriter always returns an error on Write.
type failWriter struct{}

func (f *failWriter) Write(p []byte) (int, error) {
	return 0, http.ErrNotSupported
}
