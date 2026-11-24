package modelbreaker

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// clearRegistry empties the map itself, not just each breaker's state.
//
// ResetAll keeps every entry and only closes it, which is right for production
// (the pairs are still tracked and still show in the dashboard) but wrong for
// the registry tests: TestRegistryBoundsItsSize fills the map to its cap, and a
// later test asserting on which entries survive eviction must not read that
// flood.
func clearRegistry() {
	registryMu.Lock()
	clear(registry)
	registryMu.Unlock()
}

// testOptions are the defaults, with the base cooldown kept short enough that a
// test can advance a fake clock by hand instead of sleeping.
func testOptions() Options {
	return Options{
		FailureThreshold:       5,
		CooldownBase:           60 * time.Second,
		CooldownMax:            10 * time.Minute,
		CooldownsBeforeDisable: 3,
	}
}

// newTestBreaker returns a breaker whose clock the test controls. The pair is
// keyed on the subtest's own name, so two subtests of the same table never
// land on one registry entry and hand each other their state — the registry is
// package-level by design, exactly as internal/resilience/open-sse's is, and a
// shared key would make the second subtest read the first one's counter.
func newTestBreaker(t *testing.T, model string) (*Breaker, *time.Time) {
	t.Helper()
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	clock := &now
	b := BreakerFor(t.Name(), model, testOptions())
	b.now = func() time.Time { return *clock }
	t.Cleanup(func() { b.Reset() })
	return b, clock
}

func TestBreakerAllowsUntilThreshold(t *testing.T) {
	tests := []struct {
		name      string
		failures  int
		wantAllow bool
		wantState State
	}{
		{"no failures", 0, true, StateClosed},
		{"one below threshold", 4, true, StateClosed},
		{"threshold reached opens a cooldown", 5, false, StateCooling},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, _ := newTestBreaker(t, "threshold-model-"+tt.name)
			for range tt.failures {
				b.RecordFailure()
			}
			if got := b.Allow(); got != tt.wantAllow {
				t.Errorf("Allow() = %v, want %v (state %s)", got, tt.wantAllow, b.State())
			}
			if got := b.State(); got != tt.wantState {
				t.Errorf("State() = %q, want %q", got, tt.wantState)
			}
		})
	}
}

// State() is Breaker's production accessor, exercised directly below.

func TestBreakerCooldownEscalatesThenDisables(t *testing.T) {
	tests := []struct {
		name         string
		cycles       int
		wantState    State
		wantCooldown time.Duration
	}{
		{"first cooldown is the base", 1, StateCooling, 60 * time.Second},
		{"second cooldown doubles", 2, StateCooling, 120 * time.Second},
		// CooldownsBeforeDisable is 3, so a model that fails through its third
		// cooldown does not get a fourth window: it stops serving until an
		// operator lifts it. The escalation is three cycles, not four — the
		// third failure cycle is already the last chance.
		{"third cycle disables instead of cooling again", 3, StateDisabled, 0},
		{"further cycles stay disabled", 5, StateDisabled, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, _ := newTestBreaker(t, "escalate-model-"+tt.name)
			for range tt.cycles {
				failToThreshold(b)
			}
			if got := b.State(); got != tt.wantState {
				t.Fatalf("State() = %q, want %q", got, tt.wantState)
			}
			if got := b.RetryAfter(); got != tt.wantCooldown {
				t.Errorf("RetryAfter() = %v, want %v", got, tt.wantCooldown)
			}
		})
	}
}

// failToThreshold charges exactly enough failures to open one cooldown cycle.
func failToThreshold(b *Breaker) {
	for range b.opts.FailureThreshold {
		b.RecordFailure()
	}
}

func TestBreakerCooldownElapsesThenAdmitsProbe(t *testing.T) {
	b, clock := newTestBreaker(t, "elapse-model")
	failToThreshold(b)
	if b.Allow() {
		t.Fatal("Allow() = true inside the cooldown, want false")
	}
	// The cooldown is the base (60s): one second before it ends the model is
	// still out, at exactly the end it is back with a clean failure count so
	// the next error starts a fresh cycle rather than re-tripping instantly.
	*clock = clock.Add(59 * time.Second)
	if b.Allow() {
		t.Error("Allow() = true one second before the cooldown ends, want false")
	}
	*clock = clock.Add(1 * time.Second)
	if !b.Allow() {
		t.Fatal("Allow() = false once the cooldown elapsed, want true")
	}
	// One failure after the probe is admitted must not immediately re-open:
	// the counter reset when the cooldown elapsed.
	b.RecordFailure()
	if got := b.Allow(); !got {
		t.Error("Allow() = false after one post-cooldown failure, want true")
	}
}

func TestBreakerSuccessClearsEscalation(t *testing.T) {
	b, _ := newTestBreaker(t, "success-model")
	failToThreshold(b)
	failToThreshold(b)
	if got := b.State(); got != StateCooling {
		t.Fatalf("State() = %q, want %q before the success", got, StateCooling)
	}
	b.RecordSuccess()
	if got := b.State(); got != StateClosed {
		t.Errorf("State() = %q after a success, want %q", got, StateClosed)
	}
	if got := b.RetryAfter(); got != 0 {
		t.Errorf("RetryAfter() = %v after a success, want 0", got)
	}
	// The escalation is dropped, not just the failure count: the next cooldown
	// is the base again, not the doubled one the model had climbed to.
	failToThreshold(b)
	if got := b.RetryAfter(); got != 60*time.Second {
		t.Errorf("RetryAfter() = %v after recovery and one new cycle, want 60s", got)
	}
}

func TestBreakerDisabledOnlyClearsByReset(t *testing.T) {
	b, _ := newTestBreaker(t, "disabled-model")
	// Three cooldown cycles is what the policy allows before a disable, so
	// three is what reaches the disabled state here.
	for range 3 {
		failToThreshold(b)
	}
	if got := b.State(); got != StateDisabled {
		t.Fatalf("State() = %q, want %q", got, StateDisabled)
	}
	// A cooldown elapsing must not undo a disable: the whole point of the
	// escalation is that waiting is not going to fix this model.
	b.now()
	b.Allow()
	if got := b.State(); got != StateDisabled {
		t.Errorf("State() = %q after Allow() on a disabled breaker, want %q", got, StateDisabled)
	}
	// Nor must a success, which the model can no longer produce because it is
	// never called again.
	b.RecordSuccess()
	if got := b.State(); got != StateDisabled {
		t.Errorf("State() = %q after RecordSuccess() on a disabled breaker, want %q", got, StateDisabled)
	}
	b.Reset()
	if got := b.State(); got != StateClosed {
		t.Errorf("State() = %q after Reset(), want %q", got, StateClosed)
	}
}

func TestCooldownForCaps(t *testing.T) {
	tests := []struct {
		name  string
		opts  Options
		cycle int
		want  time.Duration
	}{
		{"base on the first cycle", testOptions(), 1, 60 * time.Second},
		{"doubles on the second", testOptions(), 2, 120 * time.Second},
		{"still doubling under the cap", testOptions(), 4, 8 * time.Minute},
		{"caps at the maximum once doubling passes it", testOptions(), 5, 10 * time.Minute},
		{"caps at the maximum far later", testOptions(), 9, 10 * time.Minute},
		{"zero cycle is clamped to the base", testOptions(), 0, 60 * time.Second},
		{"a max below the base is lifted to the base", Options{CooldownBase: 60 * time.Second, CooldownMax: time.Second}, 1, 60 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.opts.CooldownFor(tt.cycle); got != tt.want {
				t.Errorf("CooldownFor(%d) = %v, want %v", tt.cycle, got, tt.want)
			}
		})
	}
}

func TestBreakerKeySplitsBackIntoProviderAndModel(t *testing.T) {
	tests := []struct {
		name            string
		provider, model string
	}{
		{"slashy model id", "openai", "gpt-5-mini"},
		{"model id with a provider prefix already in it", "custom-node", "vendor/model-x"},
		{"no model at all", "ollama-local", "llama3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := BreakerFor(tt.provider, tt.model, testOptions())
			st := b.Status()
			if st.Provider != strings.ToLower(tt.provider) || st.Model != strings.ToLower(tt.model) {
				t.Errorf("Status() = %q/%q, want %q/%q", st.Provider, st.Model,
					strings.ToLower(tt.provider), strings.ToLower(tt.model))
			}
		})
	}
}

// The registry is keyed on a model id that arrives in the request body, so an
// unauthenticated client could otherwise create one permanent entry per distinct
// string. AGENTS.md §4.G requires the bound; this pins that it actually holds and
// that evicting an entry cannot corrupt the survivors' state.
func TestRegistryBoundsItsSize(t *testing.T) {
	clearRegistry()
	t.Cleanup(clearRegistry)

	// One more than the cap, each with a distinct model id and a distinct
	// failure stamp, so the eviction has a deterministic oldest to drop.
	// Stamps are relative to now: eviction compares against time.Now, so an
	// absolute past date would read as idle and exercise the idle rule rather
	// than the cap this test is about.
	total := maxEntries + 8
	base := time.Now()
	for i := range total {
		b := BreakerFor("flood-provider", "flood-model-"+strconv.Itoa(i), testOptions())
		b.mu.Lock()
		b.lastFailure = base.Add(time.Duration(i) * time.Second)
		b.mu.Unlock()
	}
	// Every entry has a failure stamp newer than the idle window, so the cap is
	// the only thing that can bite here — the idle rule is asserted separately.
	registryMu.Lock()
	size := len(registry)
	registryMu.Unlock()
	if size > maxEntries {
		t.Fatalf("registry holds %d entries, want at most %d — the cap is not enforced", size, maxEntries)
	}

	// Two properties, and they are different questions. The cap held (asserted
	// above). The ordering is what makes eviction safe rather than merely
	// bounded: the oldest entry must be the one that went, because dropping a
	// recently-failing pair would take a live model out of rotation for a
	// boundary condition instead of for its own behaviour.
	//
	// The checkpoint is taken *before* any BreakerFor for that pair, because
	// BreakerFor recreates what it evicts — asking for the pair again is itself
	// what put it back. Reading the map directly is the only way to distinguish
	// "evicted" from "evicted and then recreated".
	registryMu.Lock()
	_, oldestPresent := registry[Key("flood-provider", "flood-model-0")]
	_, newestPresent := registry[Key("flood-provider", "flood-model-"+strconv.Itoa(total-1))]
	registryMu.Unlock()
	if oldestPresent {
		t.Error("the oldest entry survived eviction, want it dropped first")
	}
	if !newestPresent {
		t.Error("the newest entry was evicted, want the oldest dropped first")
	}

	// A survivor still serves, which is the part that would break if eviction
	// cleared a breaker's state instead of dropping the entry.
	survivor := BreakerFor("flood-provider", "flood-model-"+strconv.Itoa(total-1), testOptions())
	if !survivor.Allow() {
		t.Error("a surviving pair is refused, want it still serving after eviction")
	}
}

// An entry nobody has failed for the retention window leaves on its own, which
// is what keeps the cap from being the only defence: a model that stops being
// called must not occupy a slot forever.
func TestRegistryEvictsIdleEntries(t *testing.T) {
	clearRegistry()
	t.Cleanup(clearRegistry)

	// Stamps relative to the clock eviction uses (time.Now), not a fixed date:
	// a live entry stamped in the past is idle by definition, which is the
	// opposite of what this test asserts.
	now := time.Now()
	idle := BreakerFor("idle-provider", "idle-model", testOptions())
	idle.mu.Lock()
	idle.lastFailure = now.Add(-2 * evictIdleFor)
	idle.mu.Unlock()

	live := BreakerFor("live-provider", "live-model", testOptions())
	live.mu.Lock()
	live.lastFailure = now
	live.mu.Unlock()

	// A fresh pair, which is what triggers the eviction pass.
	BreakerFor("trigger-provider", "trigger-model", testOptions())

	// Read the map directly rather than through BreakerFor: asking for the idle
	// pair would recreate it, which is the opposite of what is being asserted.
	registryMu.Lock()
	_, idlePresent := registry[Key("idle-provider", "idle-model")]
	_, livePresent := registry[Key("live-provider", "live-model")]
	registryMu.Unlock()
	if idlePresent {
		t.Error("an entry idle for twice the retention window survived, want it evicted")
	}
	if !livePresent {
		t.Error("a recently-failing entry was evicted as idle, want it kept")
	}
}
