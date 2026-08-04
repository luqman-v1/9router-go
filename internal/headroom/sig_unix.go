//go:build !windows

package headroom

import "syscall"

// pidAlive probes a PID with signal 0 (process.kill(pid, 0)).
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// sendSigTerm requests graceful termination (SIGTERM).
func sendSigTerm(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}

// sendSigKill force-terminates a process (SIGKILL).
func sendSigKill(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}

// detachedProcAttr detaches the child into its own session so it survives the
// parent server restart.
func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
