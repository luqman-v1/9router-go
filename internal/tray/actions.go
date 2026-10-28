package tray

import (
	"fmt"

	"9router/proxy/internal/service"
)

// Actions is the seam the GUI layer calls. Each method is one menu item, so the
// binary holds no logic of its own.
//
// Stop deliberately has two shapes rather than one: the supervised path sends
// no credential, and the verified path is the only one that may reach
// daemon.Stop(). Collapsing them into a single "stop" is what makes trays leak
// admin tokens, and §C3 exists to stop that.
type Actions struct {
	// Snapshot reads the current facts and resolves them.
	Snapshot func() Snapshot
	// Start boots the gateway through the CLI's own daemon path.
	Start func() error
	// StopSupervised stops through the init supervisor. No token is sent.
	StopSupervised func() error
	// StopVerified stops through the CLI's daemon path, which verifies the PID
	// against the recorded executable before signalling anything.
	StopVerified func() error
	// AutostartEnabled reads the supervisor's state rather than a cached value.
	AutostartEnabled func() bool
	// SetAutostart installs or removes the boot entry.
	SetAutostart func(bool) error
}

// Stop runs the permitted stop path, or explains why there is none.
//
// The refusal text goes back to the caller so the menu can show it verbatim;
// it never contains a token (§C6).
func (a Actions) Stop() error {
	snap := a.Snapshot()
	if !snap.CanStop() {
		return ErrUnknown
	}
	info, ok := service.Status()
	path, refusal := DecideStop(info, ok, socketVerified())
	switch path {
	case StopSupervised:
		if err := a.StopSupervised(); err != nil {
			return fmt.Errorf("%s: %w", RefuseSupervisorStopFail, err)
		}
		return nil
	case StopVerified:
		return a.StopVerified()
	default:
		return fmt.Errorf("%s", refusalText(refusal, snap))
	}
}

// refusalText renders a refusal for a specific snapshot. Kept here rather than
// in the constants so the port and PID interpolation lives next to the use.
func refusalText(r Refusal, snap Snapshot) string {
	switch r {
	case RefuseNoSocketOwner:
		return fmt.Sprintf(string(r), snap.Port)
	case RefuseForeignPortOwner:
		return fmt.Sprintf(string(r), snap.Port, snap.PID)
	default:
		return string(r)
	}
}

// socketVerified is the L2 seam: today only Linux can answer "who owns this
// socket", and the answer needs the port, so the real implementation lives in
// the platform file. This default is the fail-closed one.
var socketVerified = func() bool { return false }
