//go:build !windows

package tunnel

import (
	"os"
	"syscall"
)

// processAlive reports whether a PID names a running process. Signal 0 is the
// portable POSIX probe: it performs the permission and existence checks
// without delivering anything.
//
// The signal must be an actual syscall.Signal(0), not a nil os.Signal. Go's
// (*os.Process).Signal returns an error for a nil signal without issuing the
// syscall at all, so a nil probe reports every process dead — including the
// caller's own, which is the one case the answer is known in advance.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
