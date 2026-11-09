// Package modelhealth holds the automatic model-health loop: it probes the
// models an operator has already looked at, counts consecutive failures, and
// disables a model that has failed often enough to be considered dead — then
// re-enables it once a probe succeeds again.
//
// The loop is deliberately narrow about what it touches. It probes only models
// that already have a history (a manual probe verdict, or a previous failure),
// so a fresh install with a hundred-model provider costs nothing until the
// operator asks about a model. It disables only on repeated failure, so one
// transient 502 does not take a working model off the listing. And it
// re-enables only what it disabled itself, never what an operator switched off
// by hand — an operator's decision outranks the scheduler's.
package modelhealth

import (
	json "encoding/json/v2"
	"errors"
	"strings"
	"time"
)

// Scope selects which models a pass probes.
type Scope string

const (
	// ScopeHistory probes models that already carry a verdict — the ones a
	// manual Test button or an earlier pass has looked at. This is the
	// default: it costs nothing on a fresh install and covers everything the
	// operator has shown interest in.
	ScopeHistory Scope = "history"
	// ScopeFailed probes only models whose last verdict was a failure or a
	// block. Cheaper than ScopeHistory and re-enables sooner, but a model
	// nobody has probed yet is never watched.
	ScopeFailed Scope = "failed"
)

// Valid reports whether the scope is one this package understands. An unknown
// scope read from a hand-edited settings blob is treated as "do nothing"
// rather than as the default, so a typo cannot silently widen the probe set.
func (s Scope) Valid() bool {
	return s == ScopeHistory || s == ScopeFailed
}

// Config is the parsed form of the modelHealth settings key.
type Config struct {
	// Enabled is the master switch. Off by default: a loop that probes
	// upstream models and disables them must be something an operator turns
	// on, not something a fresh install does.
	Enabled bool
	// IntervalMin is how often a pass runs, in minutes. Zero means the
	// default.
	IntervalMin int
	// Threshold is how many consecutive failures disable a model. Zero means
	// the default; one is refused, because a single transient error must not
	// take a model off the listing.
	Threshold int
	// Scope selects which models are probed.
	Scope Scope
	// AutoEnable re-enables a model the scheduler itself disabled once a probe
	// succeeds again. Off by default: re-enabling is a stronger action than
	// disabling, and an operator who wants it can ask.
	AutoEnable bool
}

// Defaults. The interval matches the auto-ping tick so both loops share one
// cadence an operator can reason about; the threshold is high enough that a
// provider having a bad minute does not empty the list.
const (
	DefaultIntervalMin = 60
	DefaultThreshold   = 3
	// MaxIntervalMin bounds the stored value so a typo cannot park the loop
	// for a week.
	MaxIntervalMin = 1440
	// MaxThreshold bounds the stored value for the same reason.
	MaxThreshold = 100
	// MinThreshold is 2, not 1: a single failure is one transient error, and
	// disabling on it would take a working model off the listing.
	MinThreshold = 2
)

// SettingsKey is the settings row key the configuration lives under.
const SettingsKey = "modelHealth"

// Default returns the configuration a missing or unreadable settings row
// yields: off. An unreadable row must not make the loop probe models whose
// schedule the operator never set.
func Default() Config {
	return Config{
		Enabled:     false,
		IntervalMin: DefaultIntervalMin,
		Threshold:   DefaultThreshold,
		Scope:       ScopeHistory,
		AutoEnable:  false,
	}
}

// Interval returns the configured cadence, falling back to the default when
// the stored value is zero or out of range.
func (c Config) Interval() time.Duration {
	if c.IntervalMin <= 0 || c.IntervalMin > MaxIntervalMin {
		return DefaultIntervalMin * time.Minute
	}
	return time.Duration(c.IntervalMin) * time.Minute
}

// FailureThreshold returns the configured threshold, clamped into range. A
// stored 1 is raised to the minimum rather than honoured: the point of the
// threshold is that one error is not enough.
func (c Config) FailureThreshold() int {
	if c.Threshold < MinThreshold {
		return DefaultThreshold
	}
	if c.Threshold > MaxThreshold {
		return MaxThreshold
	}
	return c.Threshold
}

// ProbeScope returns the scope to use, falling back to the default when the
// stored value is unknown.
func (c Config) ProbeScope() Scope {
	if !c.Scope.Valid() {
		return ScopeHistory
	}
	return c.Scope
}

// ErrBadShape reports a payload whose fields are not the ones this key holds.
var ErrBadShape = errors.New("modelHealth must be an object")

// ParseConfig reads a stored value, tolerating any shape: the blob is
// hand-editable through the backup import, so a wrong type drops the field
// rather than failing the whole read.
func ParseConfig(value any) Config {
	cfg := Default()
	obj, ok := value.(map[string]any)
	if !ok {
		return cfg
	}
	if v, ok := obj["enabled"].(bool); ok {
		cfg.Enabled = v
	}
	if v, ok := obj["intervalMin"].(float64); ok {
		cfg.IntervalMin = int(v)
	}
	if v, ok := obj["threshold"].(float64); ok {
		cfg.Threshold = int(v)
	}
	if v, ok := obj["scope"].(string); ok {
		cfg.Scope = Scope(strings.TrimSpace(v))
	}
	if v, ok := obj["autoEnable"].(bool); ok {
		cfg.AutoEnable = v
	}
	return cfg
}

// MergeConfig applies a write payload on top of the stored value and returns
// the canonical value to persist. Only the fields present in the payload
// change; the rest are carried over, so a client that knows one field cannot
// wipe the others.
func MergeConfig(stored, payload any) (map[string]any, error) {
	cfg := ParseConfig(stored)
	obj, ok := payload.(map[string]any)
	if !ok {
		return nil, ErrBadShape
	}
	if v, ok := obj["enabled"].(bool); ok {
		cfg.Enabled = v
	}
	if v, ok := obj["intervalMin"].(float64); ok {
		cfg.IntervalMin = int(v)
	}
	if v, ok := obj["threshold"].(float64); ok {
		cfg.Threshold = int(v)
	}
	if v, ok := obj["scope"].(string); ok {
		scope := Scope(strings.TrimSpace(v))
		if scope != "" && !scope.Valid() {
			return nil, errors.New("scope must be \"history\" or \"failed\"")
		}
		cfg.Scope = scope
	}
	if v, ok := obj["autoEnable"].(bool); ok {
		cfg.AutoEnable = v
	}
	return cfg.toMap(), nil
}

// toMap renders the canonical stored shape. The values written are the
// effective ones, not the raw request: a threshold of 1 is stored as the
// minimum it will actually run with, so what the dashboard reads back is what
// the loop does.
func (c Config) toMap() map[string]any {
	scope := c.ProbeScope()
	return map[string]any{
		"enabled":     c.Enabled,
		"intervalMin": int(c.Interval() / time.Minute),
		"threshold":   c.FailureThreshold(),
		"scope":       string(scope),
		"autoEnable":  c.AutoEnable,
	}
}

// Marshal renders the configuration for a settings read.
func (c Config) Marshal() map[string]any {
	return c.toMap()
}

// failureKey builds the counter key for one provider/model pair. Both parts
// are lowercased because the same model reaches this loop from a connection
// row, a dashboard query and a probe verdict, and the three spellings do not
// always agree on case.
func failureKey(provider, model string) string {
	return strings.ToLower(provider) + "/" + strings.ToLower(model)
}

// Failures is the persisted counter state for one provider/model pair.
type Failures struct {
	// Count is the number of consecutive failures. A success resets it to
	// zero, which is what makes the threshold "consecutive" rather than
	// "total".
	Count int `json:"count"`
	// LastError is the most recent failure text, so the audit entry can say
	// why without a second lookup.
	LastError string `json:"lastError,omitempty"`
	// LastFailedAt is when the most recent failure was recorded, RFC3339.
	LastFailedAt string `json:"lastFailedAt,omitempty"`
	// DisabledByScheduler records that this model was switched off by the
	// loop rather than by an operator. Auto-enable only ever re-enables rows
	// carrying this flag, so an operator's manual decision is never undone.
	DisabledByScheduler bool `json:"disabledByScheduler,omitempty"`
	// ResetAt is when a block lifts, carried through from the probe verdict.
	// It is what lets the loop re-enable at the right time instead of
	// guessing.
	ResetAt string `json:"resetAt,omitempty"`
	// EverProbed records that this model has been probed at least once, by
	// the scheduler or by a manual Test. It is what makes the "failed" scope
	// reachable: a row that has never been probed is let through once, so it
	// can acquire the first failure that puts it on the watch list.
	EverProbed bool `json:"everProbed,omitempty"`
}

// RecordFailure folds one failure into the stored state and reports the new
// count. The stored value is decoded leniently: a row written by an older
// build, or hand-edited, yields a fresh counter rather than an error, because
// losing the count must not stop the loop from working.
func RecordFailure(stored any, provider, model, errText, resetAt string, now time.Time) (Failures, bool) {
	f := decodeFailures(stored)
	f.Count++
	f.EverProbed = true
	f.LastError = truncate(errText, 300)
	f.LastFailedAt = now.UTC().Format(time.RFC3339)
	if resetAt != "" {
		f.ResetAt = resetAt
	}
	return f, true
}

// RecordSuccess folds one success into the stored state. The counter resets to
// zero — that is the whole point of "consecutive" — but the
// DisabledByScheduler flag is preserved, because the model is still off and
// the loop is the one that has to turn it back on.
func RecordSuccess(stored any) Failures {
	f := decodeFailures(stored)
	f.Count = 0
	f.EverProbed = true
	f.LastError = ""
	f.LastFailedAt = ""
	return f
}

// ClearSchedulerMark drops the flag that says the loop disabled this model.
// It is called when an operator re-enables the row by hand: from that moment
// the model is the operator's, and the loop must not touch it again.
func ClearSchedulerMark(stored any) Failures {
	f := decodeFailures(stored)
	f.DisabledByScheduler = false
	f.Count = 0
	f.LastError = ""
	f.ResetAt = ""
	return f
}

// DecodeFailures reads a stored counter row.
//
// The value arrives as a JSON string from the KV store, but a caller may also
// hand over an already-decoded map (a settings read, a test). Both are
// accepted: a string is unmarshalled directly, and anything else is
// re-marshalled first. A value that fits neither shape yields a fresh counter
// rather than an error, because losing the count must not stop the loop.
func DecodeFailures(stored any) Failures {
	return decodeFailures(stored)
}

// decodeFailures is the internal form DecodeFailures exposes.
func decodeFailures(stored any) Failures {
	var f Failures
	if stored == nil {
		return f
	}
	if raw, ok := stored.(string); ok {
		if strings.TrimSpace(raw) == "" {
			return f
		}
		if err := json.Unmarshal([]byte(raw), &f); err != nil {
			return Failures{}
		}
	} else {
		raw, err := json.Marshal(stored)
		if err != nil {
			return f
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			return Failures{}
		}
	}
	if f.Count < 0 {
		f.Count = 0
	}
	return f
}

// ShouldDisable reports whether the failure count has reached the threshold.
func ShouldDisable(f Failures, threshold int) bool {
	return f.Count >= threshold
}

// AuditEntry is one line of the model-health audit log.
type AuditEntry struct {
	// Provider and Model name the row the action was taken on.
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// Action is "disabled" or "enabled".
	Action string `json:"action"`
	// Reason is the human-readable cause: the failure text for a disable, the
	// successful probe for an enable.
	Reason string `json:"reason,omitempty"`
	// Actor is "scheduler" or "operator". An operator action is logged here
	// too, so the log answers "who took this model off?" without a second
	// source.
	Actor string `json:"actor"`
	// At is when it happened, RFC3339.
	At string `json:"at"`
}

// Action values.
const (
	ActionDisabled = "disabled"
	ActionEnabled  = "enabled"
)

// Actor values.
const (
	ActorScheduler = "scheduler"
	ActorOperator  = "operator"
)

// AppendAudit adds one entry to the front of the stored log and trims it to
// maxEntries. The log is stored newest-first so a reader can take the head
// without reversing, and the trim keeps a long-running gateway from growing
// the row without bound.
func AppendAudit(stored any, entry AuditEntry, maxEntries int) []AuditEntry {
	log := DecodeAudit(stored)
	log = append([]AuditEntry{entry}, log...)
	if maxEntries > 0 && len(log) > maxEntries {
		log = log[:maxEntries]
	}
	return log
}

// DecodeAudit reads the stored log leniently: a missing or malformed row
// yields an empty log rather than an error, because the audit view has to
// render something even when the row was hand-edited.
//
// The value arrives as a JSON string from the KV store, but a caller may also
// hand over an already-decoded slice; both are accepted.
func DecodeAudit(stored any) []AuditEntry {
	if stored == nil {
		return nil
	}
	var raw []byte
	switch v := stored.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		raw = []byte(v)
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		raw = encoded
	}
	var log []AuditEntry
	if err := json.Unmarshal(raw, &log); err != nil {
		return nil
	}
	return log
}

// MaxAuditEntries bounds the stored log. Two hundred lines is enough history
// for an operator to see a pattern, and small enough that the row stays cheap
// to read on every pass.
const MaxAuditEntries = 200

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit]
}
