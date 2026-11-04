package shared

import (
	"testing"
	"time"
)

func TestStreamMetrics_TokPerSec(t *testing.T) {
	now := time.Now()
	m := &StreamMetrics{}

	// Nothing streamed: zero on every axis.
	if m.TokPerSec() != 0 || m.ElapsedMs() != 0 || m.OutputTokens != 0 {
		t.Fatalf("empty metrics must report zero: tokPerSec=%v elapsed=%d tokens=%d",
			m.TokPerSec(), m.ElapsedMs(), m.OutputTokens)
	}

	// First chunk seeds both ends.
	m.OutputTokens = 3
	m.NoteOutput(now)
	m.NoteOutput(now)
	if got := m.ElapsedMs(); got != 0 {
		t.Errorf("simultaneous chunks: elapsed=%d, want 0", got)
	}
	if got := m.TokPerSec(); got != 0 {
		t.Errorf("zero elapsed: tokPerSec=%v, want 0", got)
	}

	// A gap later: rate scales with elapsed time, max gap tracks.
	m.OutputTokens = 10
	oneSec := now.Add(time.Second)
	m.NoteOutput(oneSec)
	if got := m.ElapsedMs(); got != 1000 {
		t.Errorf("elapsed=%d, want 1000", got)
	}
	if got := m.MaxGapMs; got != 1000 {
		t.Errorf("maxGap=%d, want 1000", got)
	}
	if got := m.TokPerSec(); got != 10 {
		t.Errorf("tokPerSec=%v, want 10 (10 tok / 1 s)", got)
	}

	// An in-stream stall: two chunks far apart, then more tokens.
	m.OutputTokens = 20
	twoSec := oneSec.Add(2 * time.Second)
	m.NoteOutput(twoSec)
	if got := m.MaxGapMs; got != 2000 {
		t.Errorf("maxGap=%d, want 2000", got)
	}
	// Rate = total output tokens over total elapsed time: 20 tok / 3 s.
	if got := m.TokPerSec(); got < 6.6 || got > 6.8 {
		t.Errorf("tokPerSec=%v, want ~6.67 (20 tok / 3 s)", got)
	}
}

func TestStreamMetrics_NilReceiver(t *testing.T) {
	var m *StreamMetrics
	if got := m.TokPerSec(); got != 0 {
		t.Errorf("nil TokPerSec=%v, want 0", got)
	}
	if got := m.ElapsedMs(); got != 0 {
		t.Errorf("nil ElapsedMs=%d, want 0", got)
	}
	m.NoteOutput(time.Now()) // must not panic
}
