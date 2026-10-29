package chat

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestAddRetryJitter(t *testing.T) {
	// Zero or negative returns zero
	if got := addRetryJitter(0); got != 0 {
		t.Errorf("addRetryJitter(0) = %v, want 0", got)
	}
	if got := addRetryJitter(-time.Second); got != 0 {
		t.Errorf("addRetryJitter(-1s) = %v, want 0", got)
	}

	// Positive duration adds jitter between 100ms and 400ms
	base := time.Second
	for i := 0; i < 20; i++ {
		got := addRetryJitter(base)
		diff := got - base
		if diff < 100*time.Millisecond || diff > 400*time.Millisecond {
			t.Fatalf("addRetryJitter(1s) added %v jitter, want between 100ms and 400ms", diff)
		}
	}
}

func TestPassRetry_WaitIncludesJitter(t *testing.T) {
	p := passRetry{
		earliest:    time.Second,
		rateLimited: true,
	}

	got := p.wait()
	diff := got - time.Second
	if diff < 100*time.Millisecond || diff > 400*time.Millisecond {
		t.Errorf("p.wait() = %v, want base 1s + jitter [100ms..400ms]", got)
	}
}

func TestPassRetry_WriteErrorPublishesExactSecondsWithoutJitter(t *testing.T) {
	p := passRetry{
		earliest:    2 * time.Second,
		rateLimited: true,
	}

	rec := httptest.NewRecorder()
	cw := newCommittedResponseWriter(rec)
	ue := &upstreamError{
		StatusCode: http.StatusTooManyRequests,
		Body:       []byte(`{"error":{"message":"rate limited"}}`),
	}

	p.writeError(cw, ue)

	if got := rec.Header().Get("Retry-After"); got != "2" {
		t.Errorf("Retry-After header = %q, want exact 2 seconds (no jitter leaked to client)", got)
	}

	var sec int
	var err error
	if sec, err = strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || sec != 2 {
		t.Errorf("unparseable or mismatched Retry-After: %v, sec=%d", err, sec)
	}
}
