//go:build windows

package updater

// signalSelfShutdown is a no-op on Windows: there is no portable POSIX
// SIGTERM-self, so RestartSelf falls back to os.Exit(0) after spawning the
// new process.
func signalSelfShutdown() bool {
	return false
}
