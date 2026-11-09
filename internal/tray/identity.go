// Package tray is the logic behind the 9router-tray companion: what state the
// gateway is in, and which stop paths are permitted from an unprivileged
// desktop process.
//
// It holds no GUI code. The binary in cmd/9router-tray owns the systray event
// loop and calls in here, so every decision below is testable without a
// display.
//
// The rules come from docs/specs/tray-companion-architecture.md. The invariants
// that matter for this file are:
//
//	C2 — Identity Before Control: port + PID + data scope are verified before
//	     any control action.
//	C3 — No Token To A Foreign Process: the CLI token never leaves this process
//	     unless socket-owner identity holds, and today that is Linux-only.
package tray

import (
	"9router/proxy/internal/service"
)

// StopPath says how, or whether, this process may stop the gateway.
type StopPath int

const (
	// StopRefused means no verified path exists. The tray shows why and offers
	// the dashboard instead.
	StopRefused StopPath = iota
	// StopSupervised means the gateway is owned by an init supervisor. Stopping
	// goes through service.Stop(), which sends no token: systemd/launchd/openrc/
	// runit all deliver a signal the gateway already drains on.
	StopSupervised
	// StopVerified means the gateway is not supervised but its socket owner can
	// be proven to be this machine's daemon. Only then may the CLI token travel.
	StopVerified
)

// Refusal is a machine-readable reason paired with the string the menu shows.
// The string never contains a credential: §C6.
type Refusal string

const (
	RefuseNone               Refusal = ""
	RefuseWindowsTask        Refusal = "Stop is unavailable for the windows-task backend: schtasks /end is not a graceful stop"
	RefuseNoSocketOwner      Refusal = "Stop is unavailable on this platform: the identity of the port owner cannot be verified. Use the dashboard at http://127.0.0.1:%s/dashboard"
	RefuseForeignPortOwner   Refusal = "Port %s in use by foreign process (PID %d)"
	RefuseStateUnreadable    Refusal = "Gateway state unknown — %s"
	RefuseSupervisorStopFail Refusal = "supervisor_stop_failed"
)

// DecideStop applies the two-path rule of §5.3 to the facts it is given.
//
// The shape is deliberate: supervised services never need the token, and the
// windows-task backend is refused outright because schtasks /end is a
// TerminateProcess that no drain can follow. Everything else must prove socket
// ownership, which is a Linux-only capability today — so macOS and Windows
// unsupervised installations fail closed rather than fall back to the
// claim-only path that §5.2 removed.
//
// socketVerified is passed in rather than measured here: the caller has the
// port and can run the platform-specific lookup, this function stays pure and
// unit-testable on one runner.
func DecideStop(info service.Info, ok bool, socketVerified bool) (StopPath, Refusal) {
	if ok {
		switch info.Backend {
		case service.BackendSystemdUser, service.BackendLaunchd, service.BackendOpenRC, service.BackendRunit:
			return StopSupervised, RefuseNone
		case service.BackendWindowsTask:
			// schtasks /end is not a graceful stop, and there is no L2 on
			// Windows to prove who owns the port. Refuse rather than degrade.
			return StopRefused, RefuseWindowsTask
		}
	}
	// Unsupervised, or supervised only by cron (which starts at boot but offers
	// no runtime control). Either way the token may travel only when the
	// socket owner has been proven.
	if !socketVerified {
		return StopRefused, RefuseNoSocketOwner
	}
	return StopVerified, RefuseNone
}

// IsSupervised reports whether the backend has runtime start/stop control.
// cron is excluded on purpose: it starts the gateway at boot and nothing more,
// so a "start" would do nothing and a "stop" has no addressee.
func IsSupervised(info service.Info, ok bool) bool {
	if !ok {
		return false
	}
	switch info.Backend {
	case service.BackendSystemdUser, service.BackendLaunchd, service.BackendOpenRC, service.BackendRunit:
		return true
	}
	return false
}
