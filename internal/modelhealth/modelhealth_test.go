package modelhealth

import (
	"strings"
	"testing"
	"time"
)

// TestFailureThresholdClampsToOne pins the guard that makes the threshold
// mean "consecutive": a stored 1 would disable a model on its first
// transient error, so it is raised to the default rather than honoured.
func TestFailureThresholdClampsToOne(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{name: "zero falls back to default", in: 0, want: DefaultThreshold},
		{name: "one is raised, not honoured", in: 1, want: DefaultThreshold},
		{name: "two is the minimum accepted", in: 2, want: 2},
		{name: "three passes through", in: 3, want: 3},
		{name: "absurd value is clamped", in: 9999, want: MaxThreshold},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{Threshold: tt.in}
			if got := cfg.FailureThreshold(); got != tt.want {
				t.Errorf("FailureThreshold() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestScopeValidationRejectsUnknown covers the direction that matters: an
// unknown scope read from a hand-edited settings blob must not silently widen
// the probe set.
func TestScopeValidationRejectsUnknown(t *testing.T) {
	if Scope("everything").Valid() {
		t.Error("an unknown scope must not be valid")
	}
	if !ScopeHistory.Valid() || !ScopeFailed.Valid() {
		t.Error("the two real scopes must be valid")
	}
	cfg := Config{Scope: Scope("everything")}
	if got := cfg.ProbeScope(); got != ScopeHistory {
		t.Errorf("ProbeScope() = %q, want the default %q", got, ScopeHistory)
	}
}

// TestConsecutiveFailuresResetOnSuccess is the core contract: the threshold
// counts consecutive failures, so one success clears the count.
func TestConsecutiveFailuresResetOnSuccess(t *testing.T) {
	now := time.Now()
	f, _ := RecordFailure(nil, "vsllm", "m1", "boom", "", now)
	if f.Count != 1 {
		t.Fatalf("after one failure Count = %d, want 1", f.Count)
	}
	f, _ = RecordFailure(f, "vsllm", "m1", "boom", "", now)
	if f.Count != 2 {
		t.Fatalf("after two failures Count = %d, want 2", f.Count)
	}
	f = RecordSuccess(f)
	if f.Count != 0 {
		t.Errorf("after a success Count = %d, want 0", f.Count)
	}
	if f.LastError != "" {
		t.Errorf("after a success LastError = %q, want empty", f.LastError)
	}
}

// TestSchedulerMarkSurvivesSuccess pins the flag that makes auto-enable
// possible: a success resets the counter but must not forget that the loop is
// the one that switched the model off.
func TestSchedulerMarkSurvivesSuccess(t *testing.T) {
	now := time.Now()
	f, _ := RecordFailure(nil, "vsllm", "m1", "boom", "", now)
	f.DisabledByScheduler = true

	f = RecordSuccess(f)
	if !f.DisabledByScheduler {
		t.Error("DisabledByScheduler must survive a success, or the model never comes back")
	}

	cleared := ClearSchedulerMark(f)
	if cleared.DisabledByScheduler {
		t.Error("ClearSchedulerMark must drop the flag")
	}
	if cleared.Count != 0 {
		t.Errorf("ClearSchedulerMark left Count = %d, want 0", cleared.Count)
	}
}

// TestShouldDisableUsesThreshold pins the boundary: the model goes off at the
// threshold, not one before it.
func TestShouldDisableUsesThreshold(t *testing.T) {
	if ShouldDisable(Failures{Count: 2}, 3) {
		t.Error("count 2 must not disable at threshold 3")
	}
	if !ShouldDisable(Failures{Count: 3}, 3) {
		t.Error("count 3 must disable at threshold 3")
	}
}

// TestMergeConfigCarriesUnsetFields covers the merge contract: a client that
// knows one field must not wipe the others, which is how a half-written
// settings payload used to switch the loop off.
func TestMergeConfigCarriesUnsetFields(t *testing.T) {
	stored := map[string]any{
		"enabled":     true,
		"intervalMin": float64(30),
		"threshold":   float64(5),
		"scope":       "failed",
		"autoEnable":  true,
	}
	merged, err := MergeConfig(stored, map[string]any{"threshold": float64(4)})
	if err != nil {
		t.Fatalf("MergeConfig: %v", err)
	}
	if merged["threshold"] != 4 {
		t.Errorf("threshold = %v, want 4", merged["threshold"])
	}
	if merged["enabled"] != true {
		t.Error("enabled must be carried over untouched")
	}
	if merged["intervalMin"] != 30 {
		t.Errorf("intervalMin = %v, want 30 carried over", merged["intervalMin"])
	}
	if merged["scope"] != "failed" {
		t.Errorf("scope = %v, want \"failed\" carried over", merged["scope"])
	}
}

// TestMergeConfigRejectsUnknownScope covers validation on write rather than on
// read: a typo in the dashboard is refused here, so it cannot reach the loop.
func TestMergeConfigRejectsUnknownScope(t *testing.T) {
	_, err := MergeConfig(nil, map[string]any{"scope": "everything"})
	if err == nil {
		t.Fatal("an unknown scope must be refused on write")
	}
	if !strings.Contains(err.Error(), "scope") {
		t.Errorf("error = %v, want it to name the scope field", err)
	}
}

// TestMergeConfigWritesEffectiveValues pins that what the dashboard reads back
// is what the loop does: a threshold of 1 is stored as the minimum it will
// actually run with.
func TestMergeConfigWritesEffectiveValues(t *testing.T) {
	merged, err := MergeConfig(nil, map[string]any{"threshold": float64(1)})
	if err != nil {
		t.Fatalf("MergeConfig: %v", err)
	}
	if merged["threshold"] != DefaultThreshold {
		t.Errorf("stored threshold = %v, want the effective %d", merged["threshold"], DefaultThreshold)
	}
}

// TestParseConfigToleratesJunk covers the hand-editable blob: a wrong type
// drops the field rather than failing the whole read, so one bad entry cannot
// stop the loop from starting.
func TestParseConfigToleratesJunk(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want Config
	}{
		{name: "nil yields the default", in: nil, want: Default()},
		{name: "a string yields the default", in: "nonsense", want: Default()},
		{name: "wrong field types are dropped", in: map[string]any{"enabled": "yes", "threshold": "three"}, want: Default()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseConfig(tt.in)
			if got != tt.want {
				t.Errorf("ParseConfig() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestDefaultIsOff is the safety property: a fresh install must not probe
// upstream models or disable anything until an operator turns the loop on.
func TestDefaultIsOff(t *testing.T) {
	if Default().Enabled {
		t.Error("the default must be disabled")
	}
	if Default().AutoEnable {
		t.Error("auto-enable must default to off")
	}
}

// TestAppendAuditNewestFirstAndBounded covers both properties the audit view
// relies on: the newest entry is at the head, and the row cannot grow without
// bound on a long-running gateway.
func TestAppendAuditNewestFirstAndBounded(t *testing.T) {
	now := time.Now()
	var stored any
	for range MaxAuditEntries + 20 {
		stored = AppendAudit(stored, AuditEntry{
			Provider: "vsllm", Model: "m", Action: ActionDisabled,
			Reason: "boom", Actor: ActorScheduler, At: now.Format(time.RFC3339),
		}, MaxAuditEntries)
	}
	log := DecodeAudit(stored)
	if len(log) != MaxAuditEntries {
		t.Fatalf("audit log grew to %d, want it bounded at %d", len(log), MaxAuditEntries)
	}
	if log[0].Actor != ActorScheduler {
		t.Errorf("newest entry actor = %q, want %q", log[0].Actor, ActorScheduler)
	}
}

// TestDecodeAuditToleratesJunk covers the same hand-editable-blob property as
// the config: the audit view has to render something even when the row was
// edited into an unreadable shape.
func TestDecodeAuditToleratesJunk(t *testing.T) {
	if got := DecodeAudit("nonsense"); got != nil {
		t.Errorf("DecodeAudit(junk) = %v, want nil", got)
	}
	if got := DecodeAudit(nil); got != nil {
		t.Errorf("DecodeAudit(nil) = %v, want nil", got)
	}
}
