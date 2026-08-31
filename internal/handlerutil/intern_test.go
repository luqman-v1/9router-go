package handlerutil

import (
	"testing"
)

func TestIntern(t *testing.T) {
	s1 := Intern("claude-sonnet-4-6")
	s2 := Intern("claude-sonnet-4-6")

	if s1 != s2 {
		t.Errorf("expected identical strings")
	}

	h1 := InternHandle("claude-sonnet-4-6")
	h2 := InternHandle("claude-sonnet-4-6")

	if h1 != h2 {
		t.Errorf("expected identical handles")
	}

	if Intern("") != "" {
		t.Errorf("expected empty string for empty input")
	}
}

func TestBufferPool(t *testing.T) {
	buf := AcquireBuffer()
	if buf == nil {
		t.Fatal("expected non-nil buffer")
	}
	buf.WriteString("test payload data")
	if buf.String() != "test payload data" {
		t.Errorf("buffer content mismatch")
	}
	ReleaseBuffer(buf)

	slicePtr := AcquireByteSlice()
	if slicePtr == nil {
		t.Fatal("expected non-nil byte slice pointer")
	}
	*slicePtr = append(*slicePtr, []byte("slice data")...)
	if string(*slicePtr) != "slice data" {
		t.Errorf("slice content mismatch")
	}
	ReleaseByteSlice(slicePtr)
}
