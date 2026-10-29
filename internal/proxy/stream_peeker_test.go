package proxy

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStreamContentPeeker_EmitsContentAndCommits(t *testing.T) {
	rec := httptest.NewRecorder()
	peeker := NewStreamContentPeeker(rec)
	peeker.Header().Set("Content-Type", "text/event-stream")

	// First chunk: empty role block
	_, _ = peeker.Write([]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n"))
	if rec.Code != http.StatusOK && rec.Body.Len() > 0 {
		t.Fatalf("peeker should not have committed yet")
	}

	// Second chunk: real content
	_, _ = peeker.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"))
	if rec.Body.Len() == 0 {
		t.Fatalf("peeker should have committed once content arrived")
	}

	err := peeker.Finish()
	if err != nil {
		t.Fatalf("unexpected error on finish: %v", err)
	}
}

func TestStreamContentPeeker_ZeroContentReturnsError(t *testing.T) {
	rec := httptest.NewRecorder()
	peeker := NewStreamContentPeeker(rec)

	// Stream that ends with role + finish_reason only (no content)
	_, _ = peeker.Write([]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n"))
	_, _ = peeker.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"))
	_, _ = peeker.Write([]byte("data: [DONE]\n\n"))

	err := peeker.Finish()
	if !errors.Is(err, ErrStreamingZeroContent) {
		t.Fatalf("expected ErrStreamingZeroContent, got %v", err)
	}
	if rec.Body.Len() > 0 {
		t.Fatalf("zero-content stream should not have committed any body to underlying recorder")
	}
}
