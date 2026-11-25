package chat

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type testStatusErr struct {
	StatusCode int
}

func (e *testStatusErr) Error() string {
	return "upstream boom"
}

func TestFallbackAttemptRecorder_RecordAndSnapshot(t *testing.T) {
	r := NewFallbackAttemptRecorder()
	r.Record(FallbackAttempt{ConnectionID: "c1", Account: "a1", Provider: "p1", Model: "m1", StatusCode: 500, Error: "boom", LatencyMs: 12})
	r.Record(FallbackAttempt{ConnectionID: "c2", Account: "a2", Provider: "p2", Model: "m2", StatusCode: 503, LatencyMs: 34})

	got := r.Snapshot()
	if len(got) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(got))
	}
	if got[0].ConnectionID != "c1" || got[1].ConnectionID != "c2" {
		t.Fatalf("unexpected order: %+v", got)
	}
	if got[0].Error != "boom" {
		t.Fatalf("unexpected error: %q", got[0].Error)
	}
}

func TestFallbackAttemptRecorder_CapacityLimit(t *testing.T) {
	r := NewFallbackAttemptRecorder()
	for i := 0; i < maxFallbackAttempts+5; i++ {
		r.Record(FallbackAttempt{ConnectionID: "c"})
	}
	got := r.Snapshot()
	if len(got) != maxFallbackAttempts {
		t.Fatalf("expected %d attempts, got %d", maxFallbackAttempts, len(got))
	}
}

func TestFallbackAttemptRecorder_NilSafety(t *testing.T) {
	var r *FallbackAttemptRecorder
	r.Record(FallbackAttempt{ConnectionID: "c"})
	if r.Snapshot() != nil {
		t.Fatalf("expected nil snapshot for nil recorder")
	}
}

func TestFallbackAttemptRecorder_ContextPropagation(t *testing.T) {
	r := NewFallbackAttemptRecorder()
	ctx := WithFallbackAttemptRecorder(context.Background(), r)
	if got := FallbackAttemptRecorderFrom(ctx); got != r {
		t.Fatalf("expected same recorder from context")
	}
	if got := FallbackAttemptRecorderFrom(context.Background()); got != nil {
		t.Fatalf("expected nil when no recorder in context")
	}
}

func TestRecordAttemptFromContext(t *testing.T) {
	r := NewFallbackAttemptRecorder()

	RecordAttemptFromContext(context.Background(), "ignored", "ignored", "ignored", "ignored", 200, errors.New("ignored"), 1)
	if got := r.Snapshot(); got != nil {
		t.Fatalf("expected no recorded attempt without recorder, got %+v", got)
	}

	ctx := WithFallbackAttemptRecorder(context.Background(), r)
	RecordAttemptFromContext(ctx, "c1", "a1", "p1", "m1", 0, &testStatusErr{StatusCode: 429}, 10)
	RecordAttemptFromContext(ctx, "c2", "a2", "p2", "m2", 503, errors.New("plain failure"), 20)
	RecordAttemptFromContext(ctx, "c3", "a3", "p3", "m3", 0, errors.New("unknown failure"), 30)
	RecordAttemptFromContext(ctx, "c4", "a4", "p4", "m4", 204, nil, 40)

	got := r.Snapshot()
	if len(got) != 4 {
		t.Fatalf("expected 4 attempts, got %d", len(got))
	}
	if got[0].StatusCode != 429 || got[1].StatusCode != 503 || got[2].StatusCode != 500 || got[3].StatusCode != 204 {
		t.Fatalf("unexpected status codes: %+v", got)
	}
	if got[3].Error != "" {
		t.Fatalf("expected no error text for nil error, got %q", got[3].Error)
	}
	maxLen := maxPersistedErrorLen + len("...[truncated]")
	for i := 0; i < 3; i++ {
		if got[i].Error == "" {
			t.Fatalf("expected error text for attempt %d", i)
		}
		if len(got[i].Error) > maxLen {
			t.Fatalf("error text too long for attempt %d: %d", i, len(got[i].Error))
		}
	}
}

func TestRecordAttemptFromContext_KeepsUnrecognisedUpstreamBody(t *testing.T) {
	r := NewFallbackAttemptRecorder()
	ctx := WithFallbackAttemptRecorder(context.Background(), r)

	// Cloudflare answers an unsupported tool round-trip with a JSON error
	// shape extractErrorText does not recognise. Before the length bump the
	// wrapper prefix consumed all 160 runes and the body was lost entirely.
	body := []byte(`{"success":false,"errors":[{"code":7003,"message":"cf tool round-trip rejected"}]}`)
	RecordAttemptFromContext(ctx, "cf", "a", "cloudflare-ai", "@cf/meta/llama-3.1-8b-instruct-fp8-fast", 0,
		&upstreamError{StatusCode: 400, Body: body}, 1)

	got := r.Snapshot()
	if len(got) != 1 {
		t.Fatalf("expected 1 attempt, got %d", len(got))
	}
	if !strings.Contains(got[0].Error, "cf tool round-trip rejected") {
		t.Fatalf("upstream body missing from recorded error: %q", got[0].Error)
	}
	if got[0].StatusCode != 400 {
		t.Fatalf("expected status 400, got %d", got[0].StatusCode)
	}
}

func TestRecordAttemptFromContext_TruncatesLongUpstreamBody(t *testing.T) {
	r := NewFallbackAttemptRecorder()
	ctx := WithFallbackAttemptRecorder(context.Background(), r)

	long := strings.Repeat("x", maxPersistedErrorLen+100)
	RecordAttemptFromContext(ctx, "cf", "a", "p", "m", 0,
		&upstreamError{StatusCode: 400, Body: []byte(long)}, 1)

	got := r.Snapshot()
	if len(got) != 1 {
		t.Fatalf("expected 1 attempt, got %d", len(got))
	}
	if !strings.HasSuffix(got[0].Error, "...[truncated]") {
		t.Fatalf("expected truncation suffix, got %q", got[0].Error)
	}
	if len(got[0].Error) > maxPersistedErrorLen+len("...[truncated]") {
		t.Fatalf("error text too long: %d", len(got[0].Error))
	}
}
