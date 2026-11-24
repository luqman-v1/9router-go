package modelbreaker

import (
	"strings"
	"testing"
	"time"
)

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
