//go:build !windows

package proc

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// pidAlive probes a PID with signal 0 (process.kill(pid, 0)) and reaps/checks zombies.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// If the process is a direct child of the current process, check and reap non-blockingly.
	var ws syscall.WaitStatus
	wpid, err := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
	if err == nil && wpid == pid {
		return false
	}
	if wpid == 0 {
		return true
	}

	err = syscall.Kill(pid, 0)
	if err != nil && err != syscall.EPERM {
		return false
	}

	// On Linux, a terminated child process lingers as a zombie in the process
	// table until wait() is called, but syscall.Kill(pid, 0) still returns nil.
	// Inspect /proc/<pid>/stat when available so zombie state reads as dead.
	if data, rerr := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); rerr == nil {
		if idx := bytes.LastIndexByte(data, ')'); idx != -1 && idx+2 < len(data) {
			state := data[idx+2]
			if state == 'Z' || state == 'X' {
				return false
			}
		}
	}
	return true
}

// requestStop sends SIGTERM, the port's "drain in-flight work and exit" signal.
func requestStop(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}

// forceKill sends SIGKILL.
func forceKill(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}

// resolveLink expands symlinks so a respawn execs the real file.
func resolveLink(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}

// detached puts the child in its own session so it survives the parent and the
// closing of the terminal that started it.
func detached() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// waitExit polls until the PID is gone or the budget runs out.
func waitExit(pid int, waitMS int) bool {
	deadline := time.Now().Add(time.Duration(waitMS) * time.Millisecond)
	for {
		if !pidAlive(pid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}
