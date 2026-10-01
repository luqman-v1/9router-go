package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/log"
	"9router/proxy/internal/proc"
)

// The pid file carries the executable the daemon was launched from next to its
// PID, on one line:
//
//	20130 /usr/local/bin/9router-go
//
// The PID alone is not a claim on a process: the OS recycles PIDs, so a file
// left behind by a daemon that was killed can name an unrelated program by the
// time the next command runs, and `stop` would signal that stranger. The
// executable is the tie between the number and the process — cheap to read on
// every platform, needs no privileges, and stops matching the moment the PID
// lands on something else. The start time would also tell two runs of the same
// binary apart, but it is not cheap to read portably — Linux reports a zero
// start time in /proc, Windows needs QueryProcessCycleTime — and a self-updated
// daemon runs from a binary path that has since been replaced, so an executable
// that no longer resolves makes the claim unverifiable rather than wrong.
const pidFieldSep = " "

// claim is what the pid file asserts. A zero exePath is a bare number: it still
// parses, but it cannot be verified, so nothing may be signalled on its word.
type claim struct {
	pid     int
	exePath string
}

// writePID records the running daemon PID and the binary it runs, replacing a
// stale file. The write lands whole or not at all: a half-written line would
// read back as unverifiable and strand a daemon that is up.
func writePID(pid int) error {
	if err := os.MkdirAll(Dir(), constants.FilePermDir); err != nil {
		return err
	}
	return os.WriteFile(PIDFile(), formatPID(pid), constants.FilePermFile)
}

// clearPID removes the pid file.
func clearPID() {
	if err := os.Remove(PIDFile()); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Warn("daemon", "remove pid file failed", "path", PIDFile(), "error", err)
	}
}

// formatPID renders the claim, falling back to the bare number when this
// binary's own path cannot be resolved: writing a placeholder that can never
// verify would make the daemon unfindable, and the fallback at least reports
// the daemon to anything that does not kill by PID.
func formatPID(pid int) []byte {
	self, err := proc.SelfExecutable()
	if err != nil {
		log.Warn("daemon", "pid file falls back to a bare pid", "error", err)
		return []byte(strconv.Itoa(pid))
	}
	return []byte(strconv.Itoa(pid) + pidFieldSep + self)
}

// readClaim parses the pid file, tolerating the bare-number form an older build
// wrote.
func readClaim() claim {
	data, err := os.ReadFile(PIDFile())
	if err != nil {
		return claim{}
	}
	return parseClaim(string(data))
}

// parseClaim reads the on-disk line: PID, then optionally the executable.
func parseClaim(raw string) claim {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return claim{}
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil || pid <= 0 {
		return claim{}
	}
	if len(fields) < 2 {
		return claim{pid: pid}
	}
	return claim{pid: pid, exePath: fields[1]}
}

// owner reports whether the process behind this claim is still this daemon:
// alive, and running the executable the claim recorded. A claim that does not
// resolve is unverifiable, never wrong — a verified PID with a mismatched
// executable is a recycled PID wearing a real path, and stopping it would kill
// a stranger.
func (c claim) owner() bool {
	if c.pid <= 0 || c.exePath == "" {
		return false
	}
	if !proc.Alive(c.pid) {
		return false
	}
	exe, err := proc.Executable(c.pid)
	if err != nil {
		return false
	}
	return exe == c.exePath || resolvedPath(exe) == resolvedPath(c.exePath)
}

// resolvedPath expands symlinks on whichever side the filesystem allows it; a
// path that no longer exists cannot be expanded on either, so it compares as
// itself. Windows reports an image as \Device\HarddiskVolume… and macOS reports
// a symlinked install location, neither of which is the form written to the pid
// file.
func resolvedPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}
