// Package modelbreaker holds the per-model circuit breaker: it keeps one
// provider/model pair that keeps failing out of rotation for a bounded
// cooldown, escalates the cooldown on each further failure, and — after the
// configured number of cooldown cycles — stops serving that model until an
// operator lifts it.
//
// It exists because the two breakers already in the tree answer the wrong
// question for this job. internal/resilience/open-sse is per-provider: when one
// model in a healthy provider starts failing, opening the whole provider takes
// every sibling offline too. internal/modelhealth probes models on a schedule
// and disables rows; it does not watch live traffic, so a model that began
// failing thirty seconds ago still receives requests until the next pass.
//
// Two rules keep this layer from doing harm, and both are load-bearing:
//
//   - It only charges failures that are the model's own. internal/errclass
//     already tells a model-scoped failure (the model is gone, the prompt
//     does not fit) from a provider-scoped one (the host is down) and an
//     account-scoped one (429, quota). A 5xx or a transport error says the
//     provider is sick, and the provider breaker is the layer that answers
//     that — charging it here would put every model of a briefly-down
//     provider into cooldown, then disable all of them after three cycles,
//     which is the exact failure this package was written to avoid.
//   - It never disables without a trace. Every cooldown and every disable is
//     visible in Status, so an operator sees what the request path decided
//     and why, and Reset is the one way a disabled model comes back.
package modelbreaker

import (
	"strings"
	"sync"
	"time"
)

// State is one model's admission state.
type State string

const (
	// StateClosed serves normally.
	StateClosed State = "closed"
	// StateCooling sits out a cooldown and returns on its own once it elapses.
	StateCooling State = "cooling"
	// StateDisabled serves nothing until an operator calls Reset. This is the
	// state the escalation reaches: a model that has failed through every
	// cooldown cycle is not going to recover by itself, and silently
	// re-admitting it would spend the operator's quota on it.
	StateDisabled State = "disabled"
)

// Options configures a model breaker. Zero values fall back to the defaults.
type Options struct {
	// FailureThreshold is the number of consecutive model-scoped failures that
	// open a cooldown.
	FailureThreshold int
	// CooldownBase is the cooldown of the first cycle; every further cycle
	// doubles it, capped at CooldownMax.
	CooldownBase time.Duration
	// CooldownMax caps the growth, so a long-running outage does not park a
	// model for hours after the provider is already healthy again.
	CooldownMax time.Duration
	// CooldownsBeforeDisable is how many cooldown cycles a model must fail
	// through before it is disabled instead of cooled down again.
	CooldownsBeforeDisable int
}

const (
	defaultFailureThreshold       = 5
	defaultCooldownBase           = 60 * time.Second
	defaultCooldownMax            = 10 * time.Minute
	defaultCooldownsBeforeDisable = 3
)

func (o Options) withDefaults() Options {
	if o.FailureThreshold <= 0 {
		o.FailureThreshold = defaultFailureThreshold
	}
	if o.CooldownBase <= 0 {
		o.CooldownBase = defaultCooldownBase
	}
	if o.CooldownMax <= 0 {
		o.CooldownMax = defaultCooldownMax
	}
	if o.CooldownsBeforeDisable <= 0 {
		o.CooldownsBeforeDisable = defaultCooldownsBeforeDisable
	}
	if o.CooldownMax < o.CooldownBase {
		o.CooldownMax = o.CooldownBase
	}
	return o
}

// CooldownFor returns the cooldown duration of the nth failed cycle, n>=1: the
// base doubled once per cycle already served, capped. Exported so a caller can
// show the operator what the next failure will cost without sleeping.
func (o Options) CooldownFor(cycle int) time.Duration {
	o = o.withDefaults()
	if cycle < 1 {
		cycle = 1
	}
	d := o.CooldownBase
	for i := 1; i < cycle; i++ {
		d *= 2
		if d >= o.CooldownMax {
			return o.CooldownMax
		}
	}
	if d > o.CooldownMax {
		return o.CooldownMax
	}
	return d
}

// Breaker is one provider/model pair's circuit breaker.
type Breaker struct {
	mu    sync.Mutex
	name  string
	opts  Options
	state State
	// failures counts consecutive model-scoped failures since the last
	// success. It resets on success and on a cooldown elapsing, because a
	// model that just sat out a cooldown gets a fresh probe, not a carried
	// count that would re-trip on the first error.
	failures int
	// cooldowns counts how many cooldown cycles this model has failed
	// through. It survives a cooldown elapsing — that is what makes the next
	// cooldown longer — and resets only on a success or an operator Reset.
	cooldowns int
	// since is the failure count the current regime counts from: the count that
	// opened the running cooldown, or 0 for a fresh model and for a probe
	// Allow() admitted. RecordFailure opens the next cycle a full threshold
	// past it, which makes "another full threshold" mean the same thing in all
	// three cases.
	since int
	// until is when the current cooldown ends. Zero when not cooling down.
	until       time.Time
	lastFailure time.Time
	now         func() time.Time
}

// BreakerFor returns the breaker for a provider/model pair, creating it on
// first use with the given options. Later calls do not change an existing
// breaker's options, so a model's threshold stays stable once it has state.
func BreakerFor(provider, model string, opts Options) *Breaker {
	key := Key(provider, model)
	registryMu.Lock()
	defer registryMu.Unlock()
	if b, ok := registry[key]; ok {
		return b
	}
	// Bound before creating, so the map never exceeds the cap by more than the
	// one entry being added — a bound checked only after insertion would let
	// the map sit one entry over indefinitely.
	evictLocked()
	b := &Breaker{
		name:  key,
		opts:  opts.withDefaults(),
		state: StateClosed,
		now:   time.Now,
	}
	registry[key] = b
	return b
}

// Allow reports whether a request to this model may proceed. It transitions a
// cooldown that has elapsed back to closed, so the next request is the probe
// that either clears the cycle or starts the next, longer one.
func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case StateClosed:
		return true
	case StateCooling:
		if b.now().Before(b.until) {
			return false
		}
		// The cooldown elapsed: the model is back, and the next request is a probe.
		// Both since and the live failure count go back to zero, because the
		// probe is a fresh question. Keeping failures where it was would
		// charge the probe the previous outage's history — one error on the
		// probe would clear `failures < since + threshold` and open the next
		// cooldown immediately, which is the opposite of what a probe is for.
		// What survives is cooldowns, which is what makes the *next* cooldown
		// longer than this one was.
		b.state = StateClosed
		b.since = 0
		b.failures = 0
		return true
	default:
		return false
	}
}

// RecordSuccess clears the breaker. A model that answered once is serving, and
// carrying any state past that point would keep it out of rotation for a
// failure it has already recovered from.
//
// A disabled breaker is deliberately NOT cleared here. The whole point of the
// escalation is that a model which failed through every cooldown cycle is not
// going to fix itself, and a disabled model receives no traffic — so a success
// that arrives while it is disabled cannot have come from that model. It would
// have to come from a stray caller still allowed through, and honouring it
// would silently reopen the model the operator asked to be taken out of
// rotation. Reset is the one path back.
func (b *Breaker) RecordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == StateDisabled {
		return
	}
	if b.state == StateClosed && b.failures == 0 && b.cooldowns == 0 {
		return
	}
	b.state = StateClosed
	b.failures = 0
	b.cooldowns = 0
	b.since = 0
	b.until = time.Time{}
}

// RecordFailure charges one model-scoped failure. Callers must only pass
// failures that CountsTowardBreaker accepted; a provider-scoped or
// account-scoped failure charged here would put a healthy model out of
// rotation for something it did not do.
//
// A failure that arrives while the model is already cooling down counts as the
// start of the next cycle rather than being absorbed by the cooldown in
// progress: a model that is out of rotation and is still being reported as
// failing has not recovered, and treating its next threshold of failures as one
// more cycle is what makes the cooldown grow. The counter is tracked as "the
// failure count that opened the current cooldown", so the next cycle opens when
// the count has climbed another full threshold past it — the arithmetic is
// stable whether or not Allow() ran in between and reset the live counter.
func (b *Breaker) RecordFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == StateDisabled {
		return
	}
	b.failures++
	b.lastFailure = b.now()
	// One counter, one base, and the base is whatever the current regime
	// counted from. `since` is set when a cooldown opens and cleared when a
	// probe is admitted, so "another full threshold" means the same thing in
	// both regimes:
	//
	//   - first use: since is 0, so the plain threshold opens the first
	//     cooldown;
	//   - while cooling: since is the count that opened it, so the next
	//     threshold opens the next, longer cooldown;
	//   - after a probe: since is 0 again, because the probe is a fresh
	//     question and a model that fails once on it has not answered it.
	//
	// Counting the earlier absolute failures toward a probe would park the
	// model on its first probe error, which is the opposite of a probe.
	if b.failures < b.since+b.opts.FailureThreshold {
		return
	}
	b.cooldowns++
	if b.cooldowns >= b.opts.CooldownsBeforeDisable {
		b.state = StateDisabled
		b.until = time.Time{}
		return
	}
	b.state = StateCooling
	b.since = b.failures
	b.until = b.now().Add(b.opts.CooldownFor(b.cooldowns))
}

// Reset closes the breaker by hand. It is the only way a disabled model serves
// again, and it drops the escalation so the model starts from the first cycle
// rather than the one that got it disabled.
func (b *Breaker) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state = StateClosed
	b.failures = 0
	b.cooldowns = 0
	b.since = 0
	b.until = time.Time{}
}

// Status is a snapshot for the dashboard and the logs.
type Status struct {
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	State     State  `json:"state"`
	Failures  int    `json:"failures"`
	Cooldowns int    `json:"cooldowns"`
	// RetryAfter is the wait in milliseconds, matching the provider breaker's
	// snapshot field (usagetracker.BreakerSnapshot.RetryAfterMs) so the
	// dashboard renders the two layers with one formatter. Zero means "not
	// waiting" — a closed pair, and a disabled one, which does not come back
	// on a timer.
	RetryAfter int64 `json:"retryAfterMs"`
}

// State reports the current state. It is production API, not a test
// convenience: the dashboard's per-model panel reads it to label a row
// "cooling" or "disabled", and the request path logs it when it refuses.
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// RetryAfter is how long until the breaker admits a probe, or 0 when it is not
// cooling down.
func (b *Breaker) RetryAfter() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state != StateCooling {
		return 0
	}
	d := b.until.Sub(b.now())
	if d < 0 {
		return 0
	}
	return d
}

// Status returns a snapshot of the breaker. Provider and Model are split back
// out of the registry key so a dashboard row names them the way an operator
// typed them, rather than showing an internal composite key.
func (b *Breaker) Status() Status {
	b.mu.Lock()
	provider, model := splitName(b.name)
	state, failures, cooldowns, until := b.state, b.failures, b.cooldowns, b.until
	now := b.now()
	b.mu.Unlock()
	retry := int64(0)
	if state == StateCooling && until.After(now) {
		retry = until.Sub(now).Milliseconds()
	}
	return Status{
		Provider:   provider,
		Model:      model,
		State:      state,
		Failures:   failures,
		Cooldowns:  cooldowns,
		RetryAfter: retry,
	}
}

// lastFailureTime reads the last failure stamp under the breaker's own lock.
//
// It exists for the registry's eviction, which runs under registryMu and must
// not take a breaker's lock to decide which entry is oldest. Dropping an entry
// loses nothing a breaker can recreate: the next BreakerFor starts from the
// first cooldown cycle, so the cost of evicting a live pair is one warm cooldown
// rather than a permanently cold one.
func (b *Breaker) lastFailureTime() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastFailure
}

// splitName recovers the provider and model halves of a registry key. The key
// is built by lowercasing both, so this is for display and for grouping in the
// dashboard, never for a routing decision — routing uses the key itself.
func splitName(key string) (provider, model string) {
	if i := strings.Index(key, "/"); i >= 0 {
		return key[:i], key[i+1:]
	}
	return key, ""
}
