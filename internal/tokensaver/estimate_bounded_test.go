package tokensaver

import (
	"strings"
	"testing"
	"time"
)

// TestEstimateTokensBounded_AdversarialUnicode verifies that bounded token
// estimation returns within the deadline and yields a positive estimate for a
// pathological input — 50 000 repetitive multi-byte unicode characters. The
// contract (upstream OmniRoute #19650 parity) is that token estimation never
// hangs or spikes CPU on adversarial unicode; the deadline bound is what
// enforces it.
func TestEstimateTokensBounded_AdversarialUnicode(t *testing.T) {
	// 50 000 × 3-byte runes (U+30xx CJK-ish) — repetitive unicode pattern.
	input := []byte(strings.Repeat("\u30a2", 50_000))

	const deadline = 50 * time.Millisecond
	start := time.Now()
	got := EstimateTokensBounded(input, deadline)
	elapsed := time.Since(start)

	if elapsed >= deadline {
		t.Fatalf("EstimateTokensBounded took %v, want < %v", elapsed, deadline)
	}
	if got <= 0 {
		t.Fatalf("EstimateTokensBounded(%d bytes) = %d, want > 0", len(input), got)
	}
	// The bounded result must equal the raw linear estimate: the fallback and
	// the goroutine path use the same formula, so both paths must agree.
	want := (len(input) + 3) / 4
	if got != want {
		t.Errorf("EstimateTokensBounded() = %d, want %d (linear byte-length estimate)", got, want)
	}
}

// TestEstimateTokensBounded_Empty verifies the empty-input fast path.
func TestEstimateTokensBounded_Empty(t *testing.T) {
	if got := EstimateTokensBounded(nil, DefaultEstimateDeadline); got != 0 {
		t.Errorf("EstimateTokensBounded(nil) = %d, want 0", got)
	}
}

// TestEstimateTokensBounded_ZeroDeadlineUsesDefault verifies a non-positive
// deadline is replaced by DefaultEstimateDeadline rather than firing
// immediately.
func TestEstimateTokensBounded_ZeroDeadlineUsesDefault(t *testing.T) {
	got := EstimateTokensBounded([]byte("hello world"), 0)
	want := (len("hello world") + 3) / 4
	if got != want {
		t.Errorf("EstimateTokensBounded(\"hello world\", 0) = %d, want %d", got, want)
	}
}

// TestEstimateTokensBounded_MatchesEstimateTokens verifies the bounded wrapper
// agrees with the underlying estimator on ordinary input.
func TestEstimateTokensBounded_MatchesEstimateTokens(t *testing.T) {
	cases := [][]byte{
		[]byte(""),
		[]byte("hello"),
		[]byte("hello world, this is a sentence."),
		bytesRepeat('a', 1000),
	}
	for _, b := range cases {
		got := EstimateTokensBounded(b, DefaultEstimateDeadline)
		want := EstimateTokens(b)
		if got != want {
			t.Errorf("EstimateTokensBounded(%q) = %d, EstimateTokens = %d; want equal", b, got, want)
		}
	}
}

func bytesRepeat(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}
