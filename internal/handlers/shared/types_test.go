package shared

import (
	"testing"
)

func TestResponseBuf_Reset(t *testing.T) {
	b := &ResponseBuf{}
	data := make([]byte, ResponseCaptureMax)
	for i := range data {
		data[i] = 'a'
	}
	n, err := b.Write(data)
	if err != nil || n != len(data) {
		t.Fatalf("write failed: n=%d err=%v", n, err)
	}
	if b.Len() != ResponseCaptureMax {
		t.Fatalf("expected len %d, got %d", ResponseCaptureMax, b.Len())
	}
	// writing more should be dropped (done flag set)
	_, _ = b.Write([]byte("more"))
	if b.String() != string(data) {
		t.Error("expected no extra content after cap")
	}
	b.Reset()
	if b.Len() != 0 {
		t.Errorf("expected len 0 after reset, got %d", b.Len())
	}
	if b.String() != "" {
		t.Errorf("expected empty string after reset, got %q", b.String())
	}
	// done flag reset: can write again
	n, err = b.Write([]byte("abc"))
	if err != nil || n != 3 {
		t.Fatalf("write after reset failed: n=%d err=%v", n, err)
	}
	if b.String() != "abc" {
		t.Errorf("expected abc, got %q", b.String())
	}
}
