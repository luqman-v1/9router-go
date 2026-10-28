package modelhealth

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// fakeStore is an in-memory Store for the loop tests. It records the disabled
// lists it was asked to write, so a test can assert what the loop did rather
// than what it logged.
type fakeStore struct {
	kv       map[string]string
	disabled map[string][]string
	// writeErr makes every SetKV fail, for the paths that must not panic.
	writeErr error
}

func newFakeStore() *fakeStore {
	return &fakeStore{kv: map[string]string{}, disabled: map[string][]string{}}
}

func (s *fakeStore) GetKV(scope, key string) (string, error) {
	return s.kv[scope+"|"+key], nil
}

func (s *fakeStore) SetKV(scope, key, value string) error {
	if s.writeErr != nil {
		return s.writeErr
	}
	s.kv[scope+"|"+key] = value
	return nil
}

func (s *fakeStore) DisabledModels(provider string) ([]string, error) {
	return s.disabled[provider], nil
}

func (s *fakeStore) SaveDisabledModels(provider string, ids []string) error {
	s.disabled[provider] = ids
	return nil
}

// fixedClock makes the loop's timestamps deterministic.
type fixedClock struct{ t time.Time }

func (c fixedClock) now() time.Time { return c.t }

func newTestLoop(store Store, probe ProbeFunc, candidates []Candidate, cfg Config) *Loop {
	l := NewLoop(store, probe, func() []Candidate { return candidates }, func() Config { return cfg })
	l.now = fixedClock{time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)}.now
	return l
}

// TestLoopDisablesAtThreshold is the core behaviour: the model stays on until
// the threshold is reached, then goes off and is marked so auto-enable can
// undo it.
func TestLoopDisablesAtThreshold(t *testing.T) {
	store := newFakeStore()
	calls := 0
	loop := newTestLoop(store, func(ctx context.Context, provider, model string) Verdict {
		calls++
		return Verdict{Error: "HTTP 502: upstream error"}
	}, []Candidate{{Provider: "vsllm", Model: "m1"}}, Config{Enabled: true, Threshold: 3, Scope: ScopeHistory})

	loop.pass(context.Background())
	if got := len(store.disabled["vsllm"]); got != 0 {
		t.Fatalf("after 1 failure disabled = %v, want nothing yet", store.disabled["vsllm"])
	}

	loop.pass(context.Background())
	if got := len(store.disabled["vsllm"]); got != 0 {
		t.Fatalf("after 2 failures disabled = %v, want nothing yet (threshold is 3)", store.disabled["vsllm"])
	}

	loop.pass(context.Background())
	if got := store.disabled["vsllm"]; len(got) != 1 || got[0] != "m1" {
		t.Fatalf("after 3 failures disabled = %v, want [m1]", got)
	}

	// The counter row must carry the scheduler mark, or auto-enable can never
	// distinguish this from an operator's own decision.
	f := decodeFailures(mustGet(t, store, ScopeFailures, failureKey("vsllm", "m1")))
	if !f.DisabledByScheduler {
		t.Error("the counter row must record that the scheduler disabled this model")
	}
	if calls != 3 {
		t.Errorf("probe called %d times, want 3", calls)
	}
}

// TestLoopDoesNotDisableTwice covers the duplicate-audit guard: once the model
// is off by our own hand, another failing pass must not rewrite the row and
// log a second line.
func TestLoopDoesNotDisableTwice(t *testing.T) {
	store := newFakeStore()
	loop := newTestLoop(store, func(ctx context.Context, provider, model string) Verdict {
		return Verdict{Error: "boom"}
	}, []Candidate{{Provider: "vsllm", Model: "m1"}}, Config{Enabled: true, Threshold: 2, Scope: ScopeHistory})

	for range 4 {
		loop.pass(context.Background())
	}

	log := DecodeAudit(mustGet(t, store, ScopeAudit, "log"))
	disabled := 0
	for _, e := range log {
		if e.Action == ActionDisabled {
			disabled++
		}
	}
	if disabled != 1 {
		t.Errorf("audit log has %d disable lines, want exactly 1", disabled)
	}
}

// TestLoopSuccessResetsCounter covers the "consecutive" contract end to end:
// a pass that succeeds between failures must leave the model on.
func TestLoopSuccessResetsCounter(t *testing.T) {
	store := newFakeStore()
	fail := true
	loop := newTestLoop(store, func(ctx context.Context, provider, model string) Verdict {
		if fail {
			return Verdict{Error: "boom"}
		}
		return Verdict{OK: true}
	}, []Candidate{{Provider: "vsllm", Model: "m1"}}, Config{Enabled: true, Threshold: 3, Scope: ScopeHistory})

	loop.pass(context.Background()) // 1 failure
	loop.pass(context.Background()) // 2 failures
	fail = false
	loop.pass(context.Background()) // success resets
	fail = true
	loop.pass(context.Background()) // 1 failure again
	loop.pass(context.Background()) // 2 failures

	if got := len(store.disabled["vsllm"]); got != 0 {
		t.Errorf("disabled = %v, want nothing: the success reset the count", store.disabled["vsllm"])
	}
}

// TestLoopAutoEnablesAfterBlockLifts covers the re-enable path, and the two
// conditions that gate it: the loop must be the one that disabled it, and
// auto-enable must be on.
func TestLoopAutoEnablesAfterBlockLifts(t *testing.T) {
	store := newFakeStore()
	ok := false
	loop := newTestLoop(store, func(ctx context.Context, provider, model string) Verdict {
		if ok {
			return Verdict{OK: true}
		}
		return Verdict{Error: "HTTP 429: free usage limit reached", Blocked: true, ResetAt: "2026-10-25T13:00:00Z"}
	}, []Candidate{{Provider: "vsllm", Model: "m1"}}, Config{Enabled: true, Threshold: 2, Scope: ScopeHistory, AutoEnable: true})

	loop.pass(context.Background())
	loop.pass(context.Background())
	if got := store.disabled["vsllm"]; len(got) != 1 {
		t.Fatalf("disabled = %v, want [m1] after the threshold", got)
	}

	ok = true
	loop.pass(context.Background())
	if got := len(store.disabled["vsllm"]); got != 0 {
		t.Errorf("disabled = %v, want empty after a successful probe", store.disabled["vsllm"])
	}

	f := decodeFailures(mustGet(t, store, ScopeFailures, failureKey("vsllm", "m1")))
	if f.DisabledByScheduler {
		t.Error("the scheduler mark must be cleared on re-enable")
	}
}

// TestLoopDoesNotAutoEnableWhenOff covers the default: re-enabling is a
// stronger action than disabling, so it stays off until an operator asks.
func TestLoopDoesNotAutoEnableWhenOff(t *testing.T) {
	store := newFakeStore()
	ok := false
	loop := newTestLoop(store, func(ctx context.Context, provider, model string) Verdict {
		if ok {
			return Verdict{OK: true}
		}
		return Verdict{Error: "boom"}
	}, []Candidate{{Provider: "vsllm", Model: "m1"}}, Config{Enabled: true, Threshold: 2, Scope: ScopeHistory, AutoEnable: false})

	loop.pass(context.Background())
	loop.pass(context.Background())
	ok = true
	loop.pass(context.Background())

	if got := len(store.disabled["vsllm"]); got != 1 {
		t.Errorf("disabled = %v, want the model to stay off with auto-enable off", store.disabled["vsllm"])
	}
}

// TestLoopRespectsOperatorDisable is the safety property: an operator who
// switched a model off by hand must have that decision left alone, even when
// probes start succeeding again.
func TestLoopRespectsOperatorDisable(t *testing.T) {
	store := newFakeStore()
	store.disabled["vsllm"] = []string{"m1"}
	loop := newTestLoop(store, func(ctx context.Context, provider, model string) Verdict {
		return Verdict{OK: true}
	}, []Candidate{{Provider: "vsllm", Model: "m1"}}, Config{Enabled: true, Threshold: 2, Scope: ScopeHistory, AutoEnable: true})

	loop.pass(context.Background())

	if got := store.disabled["vsllm"]; len(got) != 1 || got[0] != "m1" {
		t.Errorf("disabled = %v, want the operator's [m1] left alone", got)
	}
}

// TestOperatorEnableClearsSchedulerMark covers the hand-off: once an operator
// re-enables a row, the loop must stop treating it as its own.
func TestOperatorEnableClearsSchedulerMark(t *testing.T) {
	store := newFakeStore()
	store.disabled["vsllm"] = []string{"m1"}
	store.SetKV(ScopeFailures, failureKey("vsllm", "m1"), encode(Failures{Count: 5, DisabledByScheduler: true}))

	loop := newTestLoop(store, nil, nil, Config{Enabled: true})
	loop.RecordOperatorAction(Candidate{Provider: "vsllm", Model: "m1"}, ActionEnabled, "operator re-enabled")

	f := decodeFailures(mustGet(t, store, ScopeFailures, failureKey("vsllm", "m1")))
	if f.DisabledByScheduler {
		t.Error("a manual enable must clear the scheduler mark")
	}
	log := DecodeAudit(mustGet(t, store, ScopeAudit, "log"))
	if len(log) != 1 || log[0].Actor != ActorOperator {
		t.Errorf("audit log = %v, want one operator entry", log)
	}
}

// TestScopeFailedLetsANewModelThroughOnce covers the cold-start gap that made
// the scope unreachable: a model with no counter row is skipped forever, so it
// could never acquire the first failure that would put it on the watch list.
// One probe per new model is the entry cost, and it is paid once.
func TestScopeFailedLetsANewModelThroughOnce(t *testing.T) {
	store := newFakeStore()
	probed := map[string]int{}
	loop := newTestLoop(store, func(ctx context.Context, provider, model string) Verdict {
		probed[model]++
		return Verdict{OK: true}
	}, []Candidate{{Provider: "vsllm", Model: "brand-new"}}, Config{Enabled: true, Threshold: 3, Scope: ScopeFailed})

	loop.pass(context.Background())
	if probed["brand-new"] != 1 {
		t.Fatalf("first pass probed %d times, want 1 (the entry probe)", probed["brand-new"])
	}

	loop.pass(context.Background())
	loop.pass(context.Background())
	if probed["brand-new"] != 1 {
		t.Errorf("after the entry probe the model was probed %d times total, want 1 — a passing model must drop off the failed scope", probed["brand-new"])
	}
}

// TestScopeFailedSkipsHealthyModels covers the cheaper scope: a model whose
// last verdict was a pass is not probed again, so the loop spends nothing on
// models nobody asked about. A model with no row at all is the exception — it
// gets one entry probe, which is what lets it join the watch list.
func TestScopeFailedSkipsHealthyModels(t *testing.T) {
	store := newFakeStore()
	store.SetKV(ScopeFailures, failureKey("vsllm", "healthy"), encode(Failures{Count: 0, EverProbed: true}))
	store.SetKV(ScopeFailures, failureKey("vsllm", "broken"), encode(Failures{Count: 2, EverProbed: true}))

	probed := map[string]bool{}
	loop := newTestLoop(store, func(ctx context.Context, provider, model string) Verdict {
		probed[model] = true
		return Verdict{OK: true}
	}, []Candidate{
		{Provider: "vsllm", Model: "healthy"},
		{Provider: "vsllm", Model: "broken"},
		{Provider: "vsllm", Model: "never-seen"},
	}, Config{Enabled: true, Threshold: 3, Scope: ScopeFailed})

	loop.pass(context.Background())

	if probed["healthy"] {
		t.Error("a healthy model must not be probed under the failed scope")
	}
	if !probed["never-seen"] {
		t.Error("a model with no row gets one entry probe, or it can never join the watch list")
	}
	if !probed["broken"] {
		t.Error("a failing model must be probed under the failed scope")
	}
}

// TestScopeHistoryProbesEveryCandidate covers the default scope: unlike the
// failed scope, it does not skip a model whose last verdict was a pass — the
// candidate list itself is what carries "has history", so a model on it is
// watched whether or not it has a counter row yet.
func TestScopeHistoryProbesEveryCandidate(t *testing.T) {
	store := newFakeStore()
	store.SetKV(ScopeFailures, failureKey("vsllm", "healthy"), encode(Failures{Count: 0}))

	probed := map[string]bool{}
	loop := newTestLoop(store, func(ctx context.Context, provider, model string) Verdict {
		probed[model] = true
		return Verdict{OK: true}
	}, []Candidate{
		{Provider: "vsllm", Model: "healthy"},
		{Provider: "vsllm", Model: "never-seen"},
	}, Config{Enabled: true, Threshold: 3, Scope: ScopeHistory})

	loop.pass(context.Background())

	if !probed["healthy"] {
		t.Error("the history scope must probe a model that has a row")
	}
	if !probed["never-seen"] {
		t.Error("the history scope must probe every candidate: the list is what carries history")
	}
}

// TestLoopSkipsWhenDisabled covers the master switch: a disabled loop must not
// probe anything, even with a full candidate list.
func TestLoopSkipsWhenDisabled(t *testing.T) {
	store := newFakeStore()
	calls := 0
	loop := newTestLoop(store, func(ctx context.Context, provider, model string) Verdict {
		calls++
		return Verdict{OK: true}
	}, []Candidate{{Provider: "vsllm", Model: "m1"}}, Config{Enabled: false})

	loop.pass(context.Background())
	if calls != 0 {
		t.Errorf("probe called %d times with the loop disabled, want 0", calls)
	}
}

// TestLoopNilStoreIsInert covers the wiring failure direction: a half-built
// loop must not probe upstream models.
func TestLoopNilStoreIsInert(t *testing.T) {
	calls := 0
	loop := NewLoop(nil, func(ctx context.Context, provider, model string) Verdict {
		calls++
		return Verdict{OK: true}
	}, func() []Candidate { return []Candidate{{Provider: "vsllm", Model: "m1"}} }, func() Config { return Config{Enabled: true} })

	loop.pass(context.Background())
	if calls != 0 {
		t.Errorf("probe called %d times with a nil store, want 0", calls)
	}
}

// mustGet reads one KV row and decodes it as a Failures value.
func mustGet(t *testing.T, store *fakeStore, scope, key string) any {
	t.Helper()
	raw, err := store.GetKV(scope, key)
	if err != nil {
		t.Fatalf("GetKV(%s, %s): %v", scope, key, err)
	}
	if raw == "" {
		t.Fatalf("no row stored under %s/%s", scope, key)
	}
	return raw
}

// A disabled feature must not end the loop. It used to return outright, so
// toggling the scheduler on in the dashboard did nothing until the process
// restarted — the settings said "no restart needed" and the behaviour said
// otherwise.
//
// The interval is one minute (the floor), so the test asserts the first pass
// and the goroutine's survival rather than a cadence: Run passes immediately
// when enabled, and the only thing that ends it is the context.
func TestLoopStaysAliveWhileDisabled(t *testing.T) {
	store := newFakeStore()
	var enabled atomic.Bool
	var probes atomic.Int64
	l := NewLoop(store, func(ctx context.Context, provider, model string) Verdict {
		probes.Add(1)
		return Verdict{OK: true}
	}, func() []Candidate {
		return []Candidate{{Provider: "vsllm", Model: "m1"}}
	}, func() Config {
		cfg := Default()
		cfg.Enabled = enabled.Load()
		return cfg
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { l.Run(ctx); close(done) }()

	// Disabled at boot: no pass, but the goroutine must still be alive.
	time.Sleep(50 * time.Millisecond)
	if probes.Load() != 0 {
		t.Fatalf("probes = %d while disabled, want 0", probes.Load())
	}
	select {
	case <-done:
		t.Fatal("Run returned while the feature was merely disabled; it must stay alive")
	default:
	}

	// Turning it on runs a pass without a restart.
	enabled.Store(true)
	waitFor(t, func() bool { return probes.Load() > 0 }, "probes to start after enabling")

	// Turning it off again pauses, and the goroutine survives to be re-enabled.
	enabled.Store(false)
	settled := probes.Load()
	// The loop notices the change on its 2s paused poll, so a short sleep is
	// not enough to prove the pause; the probe count must not grow while the
	// feature is off, and the goroutine must still be alive.
	time.Sleep(3 * time.Second)
	if probes.Load() != settled {
		t.Fatalf("probes moved from %d to %d while disabled, want a pause", settled, probes.Load())
	}
	select {
	case <-done:
		t.Fatal("Run returned after the feature was switched off; it must stay alive")
	default:
	}
	enabled.Store(true)
	waitFor(t, func() bool { return probes.Load() > settled }, "probes to resume after re-enabling")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}

// A loop that starts disabled must still be running when the feature is
// switched on later — the case a fresh install hits.
func TestLoopStartsDisabledThenRuns(t *testing.T) {
	store := newFakeStore()
	var enabled atomic.Bool
	var probes atomic.Int64
	l := NewLoop(store, func(ctx context.Context, provider, model string) Verdict {
		probes.Add(1)
		return Verdict{OK: true}
	}, func() []Candidate {
		return []Candidate{{Provider: "vsllm", Model: "m1"}}
	}, func() Config {
		cfg := Default()
		cfg.Enabled = enabled.Load()
		return cfg
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go l.Run(ctx)
	time.Sleep(50 * time.Millisecond)
	if probes.Load() != 0 {
		t.Fatalf("probes = %d, want 0 while disabled at boot", probes.Load())
	}
	enabled.Store(true)
	waitFor(t, func() bool { return probes.Load() > 0 }, "probes to start after enabling")
}

// waitFor polls a condition for up to two seconds. A one-minute interval means
// the loop's own cadence cannot be waited on directly, so these tests wait on
// the observable effect instead of the tick.
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
