package modelhealth

import (
	"sync"
)

// The loop is started by the app package, which owns the shutdown context and
// the probe lane, but the dashboard handler needs a handle on it to report the
// operator's own enable/disable decisions. Passing it through the router's
// constructor would mean threading it through three call sites and every test
// that mounts the dashboard, so it is published here instead — the same
// process-wide shape db.InitGlobalDatabase uses for the connection the whole
// process shares.
//
// It is a plain guarded pointer rather than a lazily-built singleton: the app
// either started the loop or it did not, and a handler that finds none records
// no operator actions rather than inventing a loop that probes nothing.
var (
	loopMu sync.RWMutex
	loop   *Loop
)

// PublishLoop records the running loop so the dashboard handler can report
// operator actions to it. Called once at boot, after the loop is built.
func PublishLoop(l *Loop) {
	loopMu.Lock()
	defer loopMu.Unlock()
	loop = l
}

// ActiveLoop returns the running loop, or nil when the feature was never
// started (a test boot, or a process built without the scheduler).
func ActiveLoop() *Loop {
	loopMu.RLock()
	defer loopMu.RUnlock()
	return loop
}

// ResetLoopForTest forgets the published loop so a test that boots the app and
// stops it does not leave a stopped loop behind for the next test to record
// actions against.
func ResetLoopForTest() {
	loopMu.Lock()
	defer loopMu.Unlock()
	loop = nil
}
