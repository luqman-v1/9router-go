package tray

import (
	"errors"
	"testing"

	"9router/proxy/internal/service"
)

// TestResolve_UnknownIsNotStopped is C5: an unanswered question must never
// render as a negative fact. If this fails, the menu offers Start on a gateway
// that may already be up, and the user gets ErrAlreadyRunning for a click they
// did not cause.
func TestResolve_UnknownIsNotStopped(t *testing.T) {
	got := Resolve(Facts{
		ServiceOK:     true,
		ServiceInfo:   service.Info{ActiveUnknown: true},
		FailedPIDRead: nil,
	}, 20131)
	if got.State != StateUnknown {
		t.Fatalf("ActiveUnknown resolved to %v, want StateUnknown", got.State)
	}
	if got.CanStart() || got.CanStop() {
		t.Errorf("Unknown offered a control action: CanStart=%v CanStop=%v", got.CanStart(), got.CanStop())
	}
}

// TestResolve_PIDReadErrorWins: a positive reading built on a failed read is a
// guess, so the failure outranks every other fact.
func TestResolve_PIDReadErrorWins(t *testing.T) {
	got := Resolve(Facts{
		DaemonPID:     4242,
		PortBusy:      true,
		FailedPIDRead: errors.New("permission denied"),
	}, 20131)
	if got.State != StateUnknown {
		t.Fatalf("failed read with a live PID resolved to %v, want StateUnknown", got.State)
	}
}

// TestResolve_StoppedWhenNoEntryInstalled pins the distinction §7.1 draws:
// service.Status() returning ok=false means "no autostart entry", which is a
// normal Stopped machine — not Unknown, and Start must be offered.
func TestResolve_StoppedWhenNoEntryInstalled(t *testing.T) {
	got := Resolve(Facts{ServiceOK: false}, 20131)
	if got.State != StateStopped {
		t.Fatalf("no autostart entry resolved to %v, want StateStopped", got.State)
	}
	if !got.CanStart() {
		t.Error("Stopped withheld Start")
	}
}

// TestResolve_PortConflictIsNotRunning: a foreign listener is not our gateway.
func TestResolve_PortConflictIsNotRunning(t *testing.T) {
	got := Resolve(Facts{PortBusy: true, ForeignPID: 999}, 20131)
	if got.State != StatePortConflict {
		t.Fatalf("foreign listener resolved to %v, want StatePortConflict", got.State)
	}
	if got.CanStart() || got.CanStop() {
		t.Errorf("PortConflict offered a control action: CanStart=%v CanStop=%v", got.CanStart(), got.CanStop())
	}
}

// TestResolve_SupervisedActiveIsRunning: with no pid file but a supervisor that
// says active, the gateway is up.
func TestResolve_SupervisedActiveIsRunning(t *testing.T) {
	got := Resolve(Facts{
		ServiceOK:   true,
		ServiceInfo: service.Info{Backend: service.BackendSystemdUser, Active: true},
	}, 20131)
	if got.State != StateRunning {
		t.Fatalf("supervised active resolved to %v, want StateRunning", got.State)
	}
	// No pid file in this arrangement, so Stop is withheld: the supervised path
	// stops through the supervisor instead.
	if got.CanStop() {
		t.Error("supervised state offered the PID-based Stop")
	}
}
