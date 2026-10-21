//go:build windows

package clientconfig

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isRetryableRenameErr reports the three Windows failures that mean "someone
// has the file open right now", not "this write is wrong". A missing share
// mode on the target (sharing violation), an opportunistic lock (lock
// violation), and an antivirus briefly denying access all clear on their own.
func isRetryableRenameErr(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
		errors.Is(err, windows.ERROR_LOCK_VIOLATION) ||
		errors.Is(err, windows.ERROR_ACCESS_DENIED)
}
