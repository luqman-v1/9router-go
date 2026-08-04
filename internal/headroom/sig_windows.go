//go:build windows

package headroom

import (
	"os"
	"syscall"
)

// pidAlive checks whether a PID exists. Windows has no signal-0 probe, so it
// uses os.FindProcess, which fails for a non-existent PID.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	_, err := os.FindProcess(pid)
	return err == nil
}

// sendSigTerm requests graceful termination. Windows has no SIGTERM; os.Kill
// is the only portable signal, so TerminateProcess is used as the fallback.
func sendSigTerm(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

// sendSigKill force-terminates a process.
func sendSigKill(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

// detachedProcAttr detaches the child. Windows has no Setsid; an empty attr
// still lets the process outlive the parent.
func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}
