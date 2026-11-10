package audit

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeStore is an in-memory entryStore + retentionStore + chainReader, so the
// recorder and Verify can be exercised without SQLite. Rows are appended in
// write order; the chain columns are whatever the recorder set on them.
type fakeStore struct {
	mu   sync.Mutex
	rows []Entry
	fail bool // when set, BatchInsert fails (fail-open test)
}

func (f *fakeStore) BatchInsert(_ context.Context, entries []Entry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return context.DeadlineExceeded
	}
	f.rows = append(f.rows, entries...)
	return nil
}
func (f *fakeStore) snapshot() []Entry {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Entry, len(f.rows))
	copy(out, f.rows)
	return out
}
func (f *fakeStore) DeleteOlderThan(_ context.Context, cutoff time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var kept []Entry
	var n int64
	for _, e := range f.rows {
		if e.CreatedAt.Before(cutoff) {
			n++
		} else {
			kept = append(kept, e)
		}
	}
	f.rows = kept
	return n, nil
}
func (f *fakeStore) ChainIDs(_ context.Context) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, e := range f.snapshot() {
		if e.ChainID != "" && !seen[e.ChainID] {
			seen[e.ChainID] = true
			out = append(out, e.ChainID)
		}
	}
	return out, nil
}
func (f *fakeStore) Chain(_ context.Context, chainID string, afterSeq int64, limit int) ([]Entry, error) {
	if limit <= 0 {
		limit = 1000
	}
	var out []Entry
	for _, e := range f.snapshot() {
		if e.ChainID == chainID && e.ChainSeq > afterSeq {
			out = append(out, e)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func TestActionForRoute(t *testing.T) {
	tests := []struct {
		method, pattern, action, res string
	}{
		{"POST", "/api/keys", "keys.create", "keys"},
		{"PUT", "/api/keys/{id}", "keys.update", "keys"},
		{"PATCH", "/api/keys/{id}", "keys.update", "keys"},
		{"DELETE", "/api/keys/{id}", "keys.delete", "keys"},
		{"POST", "/api/settings/endpoint", "settings.endpoint", "settings"},
		{"GET", "/api/settings/backup", "settings.backup.read", "settings"},
		{"POST", "/api/proxy-pools/{id}/test", "proxy-pools.test", "proxy-pools"},
	}
	for _, tt := range tests {
		a, r := ActionForRoute(tt.method, tt.pattern)
		if a != tt.action || r != tt.res {
			t.Errorf("ActionForRoute(%s %s) = (%q, %q), want (%q, %q)", tt.method, tt.pattern, a, r, tt.action, tt.res)
		}
	}
}

func TestParseTraceparent(t *testing.T) {
	trace := "0af7651916cd43dd8448eb211c80319c"
	span := "b7ad6b7169203331"
	valid := "00-" + trace + "-" + span + "-01"
	if got, gotS := ParseTraceparent(valid); got != trace || gotS != span {
		t.Errorf("ParseTraceparent(valid) = (%q, %q), want (%q, %q)", got, gotS, trace, span)
	}
	if got, gotS := ParseTraceparent("00-00000000000000000000000000000000-" + span + "-01"); got != "" || gotS != "" {
		t.Errorf("ParseTraceparent(all-zero trace) = (%q, %q), want empty", got, gotS)
	}
	if got, gotS := ParseTraceparent("garbage"); got != "" || gotS != "" {
		t.Errorf("ParseTraceparent(garbage) = (%q, %q), want empty", got, gotS)
	}
	if got, gotS := ParseTraceparent(""); got != "" || gotS != "" {
		t.Errorf("ParseTraceparent(empty) = (%q, %q), want empty", got, gotS)
	}
}

func TestOutcomeForStatus(t *testing.T) {
	tests := []struct {
		status int
		want   string
	}{
		{200, OutcomeSuccess},
		{201, OutcomeSuccess},
		{204, OutcomeSuccess},
		{400, OutcomeFailure},
		{401, OutcomeDenied},
		{403, OutcomeDenied},
		{429, OutcomeDenied},
		{500, OutcomeFailure},
		{0, OutcomeFailure},
	}
	for _, tt := range tests {
		if got := OutcomeForStatus(tt.status); got != tt.want {
			t.Errorf("OutcomeForStatus(%d) = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestSeverityFor(t *testing.T) {
	if severityFor(OutcomeSuccess) != SeverityInfo {
		t.Errorf("severityFor(success) = %q, want %q", severityFor(OutcomeSuccess), SeverityInfo)
	}
	if severityFor(OutcomeFailure) != SeverityWarn {
		t.Errorf("severityFor(failure) = %q, want %q", severityFor(OutcomeFailure), SeverityWarn)
	}
}

// writeSome records n events and forces a flush so they are in the store.
func writeSome(t *testing.T, r *Recorder, store *fakeStore, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		r.Record(Event{Action: "keys.create", ActorType: ActorDashboard, ActorID: "session:abc", Outcome: OutcomeSuccess})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r.Flush(ctx)
}

func TestRecorderChainVerifiesClean(t *testing.T) {
	store := &fakeStore{}
	r := New(store, nil, Config{Key: DeriveKey([]byte("0123456789abcdef0123456789abcdef")), FlushSize: 1})
	defer r.Stop(time.Second)
	writeSome(t, r, store, 5)
	rep, err := Verify(context.Background(), store, r.Key())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !rep.OK {
		t.Fatalf("clean chain did not verify: %+v", rep.Breaks)
	}
	if rep.Entries != 5 {
		t.Errorf("entries = %d, want 5", rep.Entries)
	}
	if rep.Chains != 1 {
		t.Errorf("chains = %d, want 1", rep.Chains)
	}
}

func TestRecorderHashChainDetectsTampering(t *testing.T) {
	store := &fakeStore{}
	r := New(store, nil, Config{Key: DeriveKey([]byte("0123456789abcdef0123456789abcdef")), FlushSize: 1})
	defer r.Stop(time.Second)
	writeSome(t, r, store, 5)
	// Edit a stored row in place: the hash no longer matches its fields.
	rows := store.snapshot()
	mid := rows[2]
	mid.Action = "keys.delete" // a different verb than what was recorded
	rows[2] = mid
	store.rows = rows
	rep, err := Verify(context.Background(), store, r.Key())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if rep.OK {
		t.Fatalf("tampered chain verified clean; tamper was not detected")
	}
	found := false
	for _, b := range rep.Breaks {
		if strings.Contains(b.Reason, "hash mismatch") {
			found = true
		}
	}
	if !found {
		t.Errorf("no hash-mismatch break reported: %+v", rep.Breaks)
	}
}

func TestRecorderChainDetectsSequenceGap(t *testing.T) {
	store := &fakeStore{}
	r := New(store, nil, Config{Key: DeriveKey([]byte("0123456789abcdef0123456789abcdef")), FlushSize: 1})
	defer r.Stop(time.Second)
	writeSome(t, r, store, 5)
	// Delete a middle row, as a rogue retention sweep or editor might.
	rows := store.snapshot()
	store.rows = append(rows[:2], rows[3:]...)
	rep, err := Verify(context.Background(), store, r.Key())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if rep.OK {
		t.Fatalf("gap chain verified clean; the missing row was not detected")
	}
	found := false
	for _, b := range rep.Breaks {
		if strings.Contains(b.Reason, "sequence gap") {
			found = true
		}
	}
	if !found {
		t.Errorf("no sequence-gap break reported: %+v", rep.Breaks)
	}
}

func TestRecordNeverBlocksWhenQueueFull(t *testing.T) {
	store := &fakeStore{}
	r := New(store, nil, Config{Key: DeriveKey([]byte("0123456789abcdef0123456789abcdef")), BufferSize: 2, FlushSize: 1000})
	defer r.Stop(time.Second)
	// Do not flush: the writer only flushes at FlushSize, which is never
	// reached. Fill past the buffer; Record must drop, not block.
	start := time.Now()
	for i := 0; i < 50; i++ {
		r.Record(Event{Action: "keys.create", ActorType: ActorDashboard})
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Record blocked for %v when the queue was full; it must drop", elapsed)
	}
	if r.Stats().Dropped == 0 {
		t.Errorf("expected dropped events when the queue overflowed, got 0")
	}
}

func TestFailedBatchDoesNotAdvanceChain(t *testing.T) {
	store := &fakeStore{}
	r := New(store, nil, Config{Key: DeriveKey([]byte("0123456789abcdef0123456789abcdef")), FlushSize: 1})
	defer r.Stop(time.Second)
	// First batch commits.
	writeSome(t, r, store, 1)
	if len(store.snapshot()) != 1 {
		t.Fatalf("expected 1 committed row, got %d", len(store.snapshot()))
	}
	// Now the store fails.
	store.fail = true
	r.Record(Event{Action: "keys.create", ActorType: ActorDashboard})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r.Flush(ctx)
	if len(store.snapshot()) != 1 {
		t.Fatalf("failed batch must not write, got %d rows", len(store.snapshot()))
	}
	if r.Stats().Failed == 0 {
		t.Errorf("expected the failed batch to be counted, got Failed=0")
	}
	// Recovery: the next batch continues the chain from the last committed
	// row, so a clean Verify passes across the gap.
	store.fail = false
	writeSome(t, r, store, 1)
	rep, err := Verify(context.Background(), store, r.Key())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !rep.OK {
		t.Errorf("chain broke across a failed batch: %+v", rep.Breaks)
	}
}

func TestNilRecorderIsSafe(t *testing.T) {
	var r *Recorder
	r.Record(Event{Action: "x"})
	r.Flush(context.Background())
	r.Stop(time.Second)
	if got := r.ChainID(); got != "" {
		t.Errorf("nil ChainID = %q, want empty", got)
	}
	if got := r.Stats(); got.Enabled {
		t.Errorf("nil Stats = %+v, want disabled", got)
	}
}

func TestRetentionSweepDeletesOldRows(t *testing.T) {
	store := &fakeStore{}
	r := New(store, nil, Config{Key: DeriveKey([]byte("0123456789abcdef0123456789abcdef")), FlushSize: 1})
	defer r.Stop(time.Second)
	writeSome(t, r, store, 2)
	// Age the rows past a 1-hour retention.
	rows := store.snapshot()
	for i := range rows {
		rows[i].CreatedAt = rows[i].CreatedAt.Add(-2 * time.Hour)
	}
	store.rows = rows
	s := NewSweeper(store, nil, time.Hour)
	s.sweep()
	if got := len(store.snapshot()); got != 0 {
		t.Errorf("sweep left %d rows, want 0", got)
	}
}
