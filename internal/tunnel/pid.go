package tunnel

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// pidFile records the cloudflared process this binary started. It is the only
// thing that lets a later process (a restart, a watchdog tick, a status poll)
// find the tunnel again, because cloudflared is spawned detached from the
// request that created it.
func pidFilePath() string { return filepath.Join(tunnelDir(), "cloudflared.pid") }

// SavePID records the child's PID. The expectedPid guard on clear is what
// stops a stale disable from deleting a newer tunnel's PID file.
func SavePID(pid int) error {
	if err := ensureTunnelDir(); err != nil {
		return fmt.Errorf("tunnel.SavePID: create dir: %w", err)
	}
	return os.WriteFile(pidFilePath(), []byte(strconv.Itoa(pid)), 0o600)
}

// LoadPID returns the recorded PID, or 0 when there is none.
func LoadPID() int {
	data, err := os.ReadFile(pidFilePath())
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		return 0
	}
	return pid
}

// ClearPID removes the PID file. When expectedPID is non-zero the file is only
// removed if it still names that process, so a disable racing a fresh enable
// cannot orphan the new tunnel's record.
func ClearPID(expectedPID int) error {
	data, err := os.ReadFile(pidFilePath())
	if err != nil {
		return nil // nothing to clear
	}
	if expectedPID != 0 {
		if pid, convErr := strconv.Atoi(string(data)); convErr == nil && pid != expectedPID {
			return nil
		}
	}
	if err := os.Remove(pidFilePath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("tunnel.ClearPID: remove: %w", err)
	}
	return nil
}

// processAlive reports whether a PID names a running process. The probe is
// platform-specific: see pid_windows.go and pid_unix.go. Signal 0 is the
// portable POSIX form — it performs the permission and existence checks
// without delivering anything.
