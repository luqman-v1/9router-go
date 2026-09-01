//go:build !windows

package updater

import (
	"os"
	"syscall"
)

// signalSelfShutdown sends SIGTERM to our own process so main's signal handler
// can drain in-flight SSE streams and close the listener gracefully. Returns
// true when the signal was delivered (caller then waits for shutdown), false
// otherwise (caller falls back to os.Exit(0)).
func signalSelfShutdown() bool {
	return syscall.Kill(os.Getpid(), syscall.SIGTERM) == nil
}
