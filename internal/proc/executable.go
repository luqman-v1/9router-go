package proc

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Executable reports the image path of the process owning pid, with symlinks
// resolved so it compares equal to SelfExecutable.
//
// This is what tells "our daemon" from "some unrelated process that happens to
// hold this PID number": a bare PID survives in a pid file after a crash, and the
// OS reuses those numbers. The daemon verifies the claim with this before it
// signals anything, so a recycled PID cannot make `stop` kill a stranger.
//
// Platform support is best-effort. Where the path cannot be read the error says
// so, and callers must treat that as "unverified" rather than as a match.
func Executable(pid int) (string, error) {
	if pid <= 0 {
		return "", fmt.Errorf("proc.Executable: invalid pid %d", pid)
	}

	path, err := executablePath(pid)
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", fmt.Errorf("proc.Executable: no image path available for pid %d on %s", pid, runtime.GOOS)
	}
	return path, nil
}

// resolveImagePath normalizes a path so two lookups of the same binary agree.
// Windows reports \Device\HarddiskVolume… prefixes and macOS reports symlinked
// install locations, neither of which matches the form os.Executable returns.
func resolveImagePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	// A vanished path still has to compare: EvalSymlinks fails on it, and the
	// caller's own binary is the common case for a process exiting mid-lookup.
	return filepath.Clean(path)
}

// sameExecutable reports whether two image paths name the same binary.
func sameExecutable(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return resolveImagePath(a) == resolveImagePath(b)
}

// selfExecutablePath is the cached answer for this process, since the daemon
// compares every claim against the same binary it is running from.
var selfExecutablePath = func() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return resolveImagePath(exe)
}()