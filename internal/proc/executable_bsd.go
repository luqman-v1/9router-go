//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package proc

import (
	"fmt"
	"strconv"
	"syscall"
)

// Darwin and the BSDs expose the image path as a per-pid sysctl. There is no
// /proc/<pid>/exe here, and QueryFullProcessImageName is Windows-only, so this
// is the platform's own answer to the same question.
func executablePath(pid int) (string, error) {
	// The value is a NUL-terminated char array, so a raw string can carry a
	// trailing NUL that never compares equal to SelfExecutable's.
	buf, err := syscall.Sysctl("kern.proc.pathname." + strconv.Itoa(pid))
	if err != nil {
		return "", fmt.Errorf("sysctl kern.proc.pathname.%d: %w", pid, err)
	}
	return trimNUL(buf), nil
}

func trimNUL(s string) string {
	for i := range len(s) {
		if s[i] == 0 {
			return s[:i]
		}
	}
	return s
}