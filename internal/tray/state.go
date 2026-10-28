package tray

import (
	"errors"
	"fmt"

	"9router/proxy/internal/service"
)

// State is what the menu renders. Four values, not two: §C5 forbids showing
// "Stopped" for a question that was never answered.
type State int

const (
	// StateRunning means a verified daemon PID, or a supervisor that says the
	// service is active and is not merely guessing.
	StateRunning State = iota
	// StateStopped means nobody is serving and nobody is claiming to.
	StateStopped
	// StateUnknown means the question could not be answered. Control actions
	// are withheld, because Start would collide with a gateway that may be up
	// and Stop could reach a stranger.
	StateUnknown
	// StatePortConflict means something holds the port but it is not provably
	// this daemon.
	StatePortConflict
)

// Facts is everything the state machine reads. Callers gather them so the
// decision itself stays pure and this package needs no display to be tested.
type Facts struct {
	// DaemonPID is daemon.RunningPID(): the claimed PID, already verified
	// against the recorded executable. 0 means "no verified daemon", not
	// "definitely not running" — see the doc comment on FailedPIDRead.
	DaemonPID int

	// ServiceInfo and ServiceOK are service.Status()'s two returns. OK is false
	// when no autostart entry is installed at all, which is a normal Stopped
	// machine, not an Unknown one.
	ServiceInfo service.Info
	ServiceOK   bool

	// PortBusy reports whether something is listening on the configured port.
	PortBusy bool

	// ForeignPID is the PID holding the port when it could be attributed to
	// something other than this daemon; 0 when unknown.
	ForeignPID int

	// FailedPIDRead is set when the pid file or the service probe returned an
	// error rather than a clean negative. A clean "no file" is not a failure.
	FailedPIDRead error
}

// Snapshot is the renderable result: the state plus the strings the menu needs.
type Snapshot struct {
	State  State
	Port   int
	PID    int
	Reason string
}

// Resolve turns facts into a state. Order matters: an unreadable input wins
// over every positive reading, because a positive built on a failed read is a
// guess.
func Resolve(f Facts, port int) Snapshot {
	if f.FailedPIDRead != nil {
		return Snapshot{State: StateUnknown, Port: port, Reason: f.FailedPIDRead.Error()}
	}

	switch {
	case f.ServiceOK && f.ServiceInfo.ActiveUnknown:
		// The backend exists but could not be interrogated. Active carries no
		// information, and printing its zero value as "not running" is the bug
		// ActiveUnknown was added to prevent.
		return Snapshot{State: StateUnknown, Port: port, Reason: "supervisor state could not be read"}

	case f.DaemonPID > 0:
		return Snapshot{State: StateRunning, Port: port, PID: f.DaemonPID}

	case f.ServiceOK && f.ServiceInfo.Active:
		// Supervised and the supervisor says it is up. There is no pid file in
		// this arrangement, so PID stays 0 — the menu shows the port instead.
		return Snapshot{State: StateRunning, Port: port}

	case f.PortBusy:
		// Something answers on the port, but it is not the daemon we know. Do
		// not render it as Running and do not offer Stop.
		return Snapshot{State: StatePortConflict, Port: port, PID: f.ForeignPID}

	default:
		return Snapshot{State: StateStopped, Port: port}
	}
}

// ErrUnknown is returned by actions attempted in a state that withholds them.
var ErrUnknown = errors.New("gateway state unknown; refusing to act")

// CanStart reports whether the Start item should be offered and acted on.
func (s Snapshot) CanStart() bool {
	return s.State == StateStopped
}

// CanStop reports whether the Stop item should be offered at all. Whether it
// will succeed is DecideStop's question, not this one.
func (s Snapshot) CanStop() bool {
	return s.State == StateRunning && s.PID > 0
}

// Title is the first, disabled menu row.
func (s Snapshot) Title() string {
	switch s.State {
	case StateRunning:
		return fmt.Sprintf("9Router Go (Port %d)", s.Port)
	case StateStopped:
		return "9Router Go (Stopped)"
	case StatePortConflict:
		return fmt.Sprintf("9Router Go (Port %d in use by foreign process (PID %d))", s.Port, s.PID)
	default:
		return "9Router Go (Unknown)"
	}
}
