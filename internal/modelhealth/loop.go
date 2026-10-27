package modelhealth

import (
	"context"
	json "encoding/json/v2"
	"time"

	"9router/proxy/internal/log"
)

// Verdict is what one probe answered, reduced to the fields this loop acts on.
//
// It is a small struct rather than the dashboard's own testModelResult because
// this loop must not depend on the media handler package: the probe travels
// through the chat lane, and the two live in different halves of the tree.
type Verdict struct {
	// OK reports a pass.
	OK bool
	// Error is the failure text, for the audit log and the counter row.
	Error string
	// Blocked reports that the account is parked and nothing was asked of the
	// model. This is the only failure kind the loop will re-enable, because it
	// is the only one that carries a reset time.
	Blocked bool
	// ResetAt is when a block lifts, RFC3339. Empty when the gateway could not
	// read one out of the upstream refusal.
	ResetAt string
}

// ProbeFunc runs one probe and returns the verdict. The caller owns *how* —
// sending a provider-specific request is a much larger surface than deciding
// a model is due, and belongs next to the executors.
type ProbeFunc func(ctx context.Context, provider, model string) Verdict

// CandidateLister returns the models a pass should consider, as
// provider/model pairs. The caller owns which models qualify: this loop only
// decides what to do with them.
type CandidateLister func() []Candidate

// Candidate is one provider/model pair a pass may probe.
type Candidate struct {
	Provider string
	Model    string
}

// Store is the persistence this loop needs. It is an interface rather than a
// concrete *db.Repo so the loop can be tested without a database, and so the
// KV scope names stay in one place.
type Store interface {
	// GetKV reads one key of one scope, returning "" when absent.
	GetKV(scope, key string) (string, error)
	// SetKV writes one key of one scope.
	SetKV(scope, key, value string) error
	// DisabledModels returns the current disabled list for one provider.
	DisabledModels(provider string) ([]string, error)
	// SaveDisabledModels writes the disabled list for one provider.
	SaveDisabledModels(provider string, ids []string) error
}

// Scope names for the KV rows this loop owns.
const (
	ScopeConfig    = "modelHealth"
	ScopeFailures  = "modelHealth.failures"
	ScopeAudit     = "modelHealth.audit"
	ScopeAuditLog  = "modelHealth.audit"
	ScopeScheduler = "modelHealth.scheduler"
)

// Loop runs the model-health passes.
type Loop struct {
	store      Store
	probe      ProbeFunc
	candidates CandidateLister
	config     func() Config
	now        func() time.Time
}

// NewLoop builds a loop. A nil store or probe yields a loop whose Run does
// nothing, which is the right failure direction: a half-wired loop must not
// probe upstream models.
func NewLoop(store Store, probe ProbeFunc, candidates CandidateLister, config func() Config) *Loop {
	return &Loop{
		store:      store,
		probe:      probe,
		candidates: candidates,
		config:     config,
		now:        time.Now,
	}
}

// Run ticks until ctx is cancelled. The first pass runs immediately so a
// process that starts inside an interval fires the pass that came due while
// it was down.
//
// A disabled feature does NOT end the loop. It used to return outright, which
// meant toggling the scheduler on in the dashboard did nothing until the
// process restarted — the settings said "no restart needed" and the behaviour
// said otherwise. The loop now stays alive and re-reads the config on a short
// cadence while the feature is off, so turning it on takes effect promptly
// rather than after a full interval; once running, it settles into the
// configured interval.
func (l *Loop) Run(ctx context.Context) {
	if l.store == nil || l.probe == nil {
		return
	}
	// disabledPoll is how often a paused loop re-reads the config. The
	// configured interval can be an hour, and waiting that long to notice the
	// operator switched the feature on is the restart the copy promises is
	// unnecessary — just a slower one.
	disabledPoll := 2 * time.Second
	wait := func() bool {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(disabledPoll):
			return true
		}
	}
	started := false
	for {
		cfg := l.currentConfig()
		if !cfg.Enabled {
			if started {
				started = false
				log.Info("modelhealth", "disabled by settings; pausing")
			}
			if !wait() {
				return
			}
			continue
		}
		if !started {
			started = true
			log.Info("modelhealth", "scheduler started",
				"interval", cfg.Interval().String(),
				"threshold", cfg.FailureThreshold(),
				"scope", string(cfg.ProbeScope()))
		}
		l.pass(ctx)
		// Sleep in short slices rather than one long wait, so a context cancel
		// or a config change is noticed promptly. The interval can be an hour,
		// and a single blocking wait would make both the shutdown and the
		// re-enable path wait it out — the restart the copy promises is
		// unnecessary, just a slower one.
		deadline := time.Now().Add(cfg.Interval())
		for time.Now().Before(deadline) {
			remaining := time.Until(deadline)
			if remaining > disabledPoll {
				remaining = disabledPoll
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(remaining):
			}
			// A shorter interval saved in the dashboard takes effect now
			// rather than after the old one elapses.
			if next := l.currentConfig(); next.Interval() < cfg.Interval() {
				break
			}
			// A feature switched off mid-interval stops the wait: the loop goes
			// back to the paused poll rather than finishing an interval nobody
			// asked it to finish.
			if !l.currentConfig().Enabled {
				break
			}
		}
	}
}

func (l *Loop) currentConfig() Config {
	if l.config == nil {
		return Default()
	}
	return l.config()
}

// pass runs one sweep over the candidate list.
//
// The nil guards are the wiring failure direction: a loop built without a
// store or without a candidate source must do nothing rather than panic on
// the first tick, because a panic in a background goroutine takes the whole
// process down.
func (l *Loop) pass(ctx context.Context) {
	if l.store == nil || l.candidates == nil {
		return
	}
	cfg := l.currentConfig()
	if !cfg.Enabled {
		return
	}
	threshold := cfg.FailureThreshold()
	scope := cfg.ProbeScope()
	for _, c := range l.candidates() {
		if ctx.Err() != nil {
			return
		}
		l.evaluate(ctx, cfg, threshold, scope, c)
	}
}

// evaluate probes one candidate and folds the verdict into the stored state.
func (l *Loop) evaluate(ctx context.Context, cfg Config, threshold int, scope Scope, c Candidate) {
	if c.Provider == "" || c.Model == "" {
		return
	}
	key := failureKey(c.Provider, c.Model)

	stored, err := l.store.GetKV(ScopeFailures, key)
	if err != nil {
		log.Warn("modelhealth", "read counter", "provider", c.Provider, "model", c.Model, "error", err)
		return
	}
	prior := decodeFailures(stored)

	// Scope filtering.
	//
	// ScopeFailed skips a model whose last verdict was a pass: the operator
	// has not asked about it and it is not known to be broken, so probing it
	// spends a request for information nobody wanted.
	//
	// The `!prior.EverProbed` escape is what keeps the scope reachable at
	// all. Without it a fresh row is skipped forever, so a model nobody has
	// probed can never acquire the first failure that would put it on the
	// watch list — the scope would only ever see rows some other path had
	// already written. One probe per new model is the entry cost, and it is
	// paid once: the row exists from then on.
	if scope == ScopeFailed && prior.Count == 0 && !prior.DisabledByScheduler && prior.EverProbed {
		return
	}

	verdict := l.probe(ctx, c.Provider, c.Model)
	now := l.now()

	if verdict.OK {
		l.onSuccess(c, key, prior)
		return
	}
	l.onFailure(c, key, prior, verdict, threshold, now, cfg)
}

// onSuccess resets the counter and, when the loop is the one that switched the
// model off and auto-enable is on, turns it back on.
func (l *Loop) onSuccess(c Candidate, key string, prior Failures) {
	updated := RecordSuccess(prior)
	if err := l.store.SetKV(ScopeFailures, key, encode(updated)); err != nil {
		log.Warn("modelhealth", "write counter", "provider", c.Provider, "model", c.Model, "error", err)
		return
	}
	if !prior.DisabledByScheduler {
		// Either the operator switched this off, or it was never off. The
		// loop does not undo an operator's decision.
		return
	}
	if !l.currentConfig().AutoEnable {
		return
	}
	l.enable(c, "probe succeeded after a block lifted")
}

// onFailure records the failure and disables the model once the threshold is
// reached.
func (l *Loop) onFailure(c Candidate, key string, prior Failures, v Verdict, threshold int, now time.Time, cfg Config) {
	updated, _ := RecordFailure(prior, c.Provider, c.Model, v.Error, v.ResetAt, now)
	if err := l.store.SetKV(ScopeFailures, key, encode(updated)); err != nil {
		log.Warn("modelhealth", "write counter", "provider", c.Provider, "model", c.Model, "error", err)
		return
	}
	if !ShouldDisable(updated, threshold) {
		return
	}
	if prior.DisabledByScheduler {
		// Already off by our own hand; disabling again would rewrite the same
		// row and log a duplicate audit line every pass.
		return
	}
	l.disable(c, v.Error)
}

// disable switches a model off and marks the counter row so auto-enable knows
// the loop is the one that has to undo it.
func (l *Loop) disable(c Candidate, reason string) {
	ids, err := l.store.DisabledModels(c.Provider)
	if err != nil {
		log.Warn("modelhealth", "read disabled", "provider", c.Provider, "error", err)
		return
	}
	if !containsString(ids, c.Model) {
		ids = append(ids, c.Model)
		if err := l.store.SaveDisabledModels(c.Provider, ids); err != nil {
			log.Warn("modelhealth", "save disabled", "provider", c.Provider, "model", c.Model, "error", err)
			return
		}
	}
	key := failureKey(c.Provider, c.Model)
	stored, _ := l.store.GetKV(ScopeFailures, key)
	f := decodeFailures(stored)
	f.DisabledByScheduler = true
	if err := l.store.SetKV(ScopeFailures, key, encode(f)); err != nil {
		log.Warn("modelhealth", "mark scheduler-disabled", "provider", c.Provider, "model", c.Model, "error", err)
	}
	l.audit(c, ActionDisabled, reason, ActorScheduler)
	log.Info("modelhealth", "model disabled", "provider", c.Provider, "model", c.Model, "reason", truncate(reason, 120))
}

// enable switches a model back on and clears the scheduler mark, so a later
// manual disable is respected.
func (l *Loop) enable(c Candidate, reason string) {
	ids, err := l.store.DisabledModels(c.Provider)
	if err != nil {
		log.Warn("modelhealth", "read disabled", "provider", c.Provider, "error", err)
		return
	}
	if containsString(ids, c.Model) {
		next := make([]string, 0, len(ids))
		for _, id := range ids {
			if id != c.Model {
				next = append(next, id)
			}
		}
		if err := l.store.SaveDisabledModels(c.Provider, next); err != nil {
			log.Warn("modelhealth", "save enabled", "provider", c.Provider, "model", c.Model, "error", err)
			return
		}
	}
	key := failureKey(c.Provider, c.Model)
	stored, _ := l.store.GetKV(ScopeFailures, key)
	f := ClearSchedulerMark(stored)
	if err := l.store.SetKV(ScopeFailures, key, encode(f)); err != nil {
		log.Warn("modelhealth", "clear scheduler mark", "provider", c.Provider, "model", c.Model, "error", err)
	}
	l.audit(c, ActionEnabled, reason, ActorScheduler)
	log.Info("modelhealth", "model re-enabled", "provider", c.Provider, "model", c.Model, "reason", reason)
}

// audit appends one line to the stored log. A write failure is logged and
// dropped: the action itself already happened, and failing the pass over a
// missing audit line would stop the loop from doing its job.
func (l *Loop) audit(c Candidate, action, reason, actor string) {
	stored, err := l.store.GetKV(ScopeAudit, "log")
	if err != nil {
		return
	}
	entry := AuditEntry{
		Provider: c.Provider,
		Model:    c.Model,
		Action:   action,
		Reason:   truncate(reason, 300),
		Actor:    actor,
		At:       l.now().UTC().Format(time.RFC3339),
	}
	entries := AppendAudit(stored, entry, MaxAuditEntries)
	if err := l.store.SetKV(ScopeAudit, "log", encode(entries)); err != nil {
		log.Warn("modelhealth", "write audit", "provider", c.Provider, "model", c.Model, "error", err)
	}
}

// RecordOperatorAction logs an action an operator took from the dashboard, so
// the log answers "who took this model off?" from one place.
//
// It also clears the scheduler mark on a manual enable: from that moment the
// model is the operator's, and the loop must not re-enable or re-disable it on
// its own judgement.
func (l *Loop) RecordOperatorAction(c Candidate, action, reason string) {
	if action == ActionEnabled {
		key := failureKey(c.Provider, c.Model)
		stored, _ := l.store.GetKV(ScopeFailures, key)
		f := ClearSchedulerMark(stored)
		if err := l.store.SetKV(ScopeFailures, key, encode(f)); err != nil {
			log.Warn("modelhealth", "clear scheduler mark", "provider", c.Provider, "model", c.Model, "error", err)
		}
	}
	l.audit(c, action, reason, ActorOperator)
}

// containsString reports whether ids contains want. It is a local helper
// rather than slices.Contains because the ids arrive from JSON and the
// comparison is exact.
func containsString(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// encode renders a value as the JSON string the KV store holds.
func encode(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(raw)
}
