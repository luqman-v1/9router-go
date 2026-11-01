//go:build windows

package tunnel

import (
	"syscall"
	"unsafe"
)

// processAlive reports whether a PID names a running process.
//
// Windows has no signals, so os.Process.Signal cannot be used as the probe it
// is on unix. OpenProcess with PROCESS_QUERY_LIMITED_INFORMATION is the
// equivalent: it succeeds only while the process exists, and the handle is
// closed immediately. A PID that has exited fails with
// ERROR_INVALID_PARAMETER, which is what makes this a liveness check rather
// than a permission check.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	_ = syscall.CloseHandle(handle)
	return true
}

// processQueryLimitedInformation is the narrowest access right that still
// lets a caller test for existence. Asking for more would fail on a process
// the gateway does not own, which would report a live tunnel as dead.
const processQueryLimitedInformation = 0x1000

var _ = unsafe.Sizeof(byte(0))
