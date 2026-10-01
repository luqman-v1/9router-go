//go:build windows

package proc

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// QueryFullProcessImageName is the Windows equivalent of reading /proc/<pid>/exe.
// It needs PROCESS_QUERY_LIMITED_INFORMATION, which a process may deny to other
// users but always grants for one it owns, which is the case that matters: the
// daemon compares its own recorded claim against a PID the OS may have recycled.
func executablePath(pid int) (string, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", fmt.Errorf("open process %d: %w", pid, err)
	}
	defer windows.CloseHandle(handle)

	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(handle, 0, &buf[0], &n); err != nil {
		return "", fmt.Errorf("query image name for pid %d: %w", pid, err)
	}
	return windows.UTF16ToString(buf[:n]), nil
}