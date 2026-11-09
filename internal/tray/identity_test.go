package tray

import (
	"strings"
	"testing"

	"9router/proxy/internal/service"
)

// TestDecideStop_SupervisedSendsNoToken is C3's positive half: the four
// signal-based supervisors drain on SIGTERM, so the token must not travel.
func TestDecideStop_SupervisedSendsNoToken(t *testing.T) {
	for _, backend := range []service.Backend{
		service.BackendSystemdUser,
		service.BackendLaunchd,
		service.BackendOpenRC,
		service.BackendRunit,
	} {
		path, refusal := DecideStop(service.Info{Backend: backend}, true, false)
		if path != StopSupervised {
			t.Errorf("backend %s decided %v, want StopSupervised", backend, path)
		}
		if refusal != RefuseNone {
			t.Errorf("backend %s refused: %q", backend, refusal)
		}
	}
}

// TestDecideStop_WindowsTaskRefuses: schtasks /end is a TerminateProcess, and
// there is no L2 on Windows, so both the supervisor path and the token path are
// closed. Refusing is the only honest answer.
func TestDecideStop_WindowsTaskRefuses(t *testing.T) {
	path, refusal := DecideStop(service.Info{Backend: service.BackendWindowsTask}, true, false)
	if path != StopRefused {
		t.Errorf("windows-task decided %v, want StopRefused", path)
	}
	if refusal != RefuseWindowsTask {
		t.Errorf("windows-task refusal = %q", refusal)
	}
}

// TestDecideStop_CronIsNotSupervised: cron starts at boot and offers no runtime
// control, so it falls through to the verified path rather than being treated as
// supervised.
func TestDecideStop_CronFallsThrough(t *testing.T) {
	path, _ := DecideStop(service.Info{Backend: service.BackendCron}, true, true)
	if path != StopVerified {
		t.Errorf("cron with socket proof decided %v, want StopVerified", path)
	}
	path, refusal := DecideStop(service.Info{Backend: service.BackendCron}, true, false)
	if path != StopRefused {
		t.Errorf("cron without socket proof decided %v, want StopRefused", path)
	}
	if refusal != RefuseNoSocketOwner {
		t.Errorf("cron refusal = %q", refusal)
	}
}

// TestDecideStop_UnsupervisedFailsClosed is the removed-L3 guard: no socket
// proof means no token, on every platform. The claim-only path must not come
// back by accident.
func TestDecideStop_UnsupervisedFailsClosed(t *testing.T) {
	for _, ok := range []bool{false, true} {
		info := service.Info{Backend: service.BackendNone}
		path, refusal := DecideStop(info, ok, false)
		if path != StopRefused {
			t.Errorf("ok=%v without socket proof decided %v, want StopRefused", ok, path)
		}
		if refusal != RefuseNoSocketOwner {
			t.Errorf("ok=%v refusal = %q", ok, refusal)
		}
	}
}

// TestRefusalText_CarriesNoSecret is C6: refusals are shown in a menu and may be
// screenshotted, so they must never carry a credential.
func TestRefusalText_CarriesNoSecret(t *testing.T) {
	snap := Snapshot{State: StatePortConflict, Port: 20131, PID: 999}
	for _, r := range []Refusal{RefuseNoSocketOwner, RefuseForeignPortOwner, RefuseWindowsTask} {
		text := refusalText(r, snap)
		lower := strings.ToLower(text)
		for _, banned := range []string{"token", "secret", "sk-", "machine-id"} {
			if strings.Contains(lower, banned) {
				t.Errorf("refusal %q contains %q: %s", r, banned, text)
			}
		}
	}
}

// TestIsSupervised_CronIsNot: the Start/Stop menu is built from this, and cron
// has no runtime control to offer.
func TestIsSupervised_CronIsNot(t *testing.T) {
	if IsSupervised(service.Info{Backend: service.BackendCron}, true) {
		t.Error("cron reported as supervised")
	}
	if !IsSupervised(service.Info{Backend: service.BackendSystemdUser}, true) {
		t.Error("systemd-user reported as unsupervised")
	}
	if IsSupervised(service.Info{}, false) {
		t.Error("no entry reported as supervised")
	}
}
