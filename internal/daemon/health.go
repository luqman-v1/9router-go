package daemon

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"9router/proxy/internal/proc"
)

// Flag names and the environment marker shared by the CLI, the child argument
// filter, and the tests.
const (
	// BackgroundName is the CLI flag name (urfave/cli rejects a leading dash).
	BackgroundName = "background"
	// BackgroundFlag is the long form as it appears in argv.
	BackgroundFlag = "--" + BackgroundName
	// BackgroundAlias is the short form as it appears in argv.
	BackgroundAlias = "-d"
	// EnvBackground marks a process started as a background daemon.
	EnvBackground = "9ROUTER_BACKGROUND"
)

// HealthPath is the endpoint Start polls; it is public and answers without a
// session or API key.
const HealthPath = "/health"

// IsBackgroundProcess reports whether this process was spawned as a daemon.
func IsBackgroundProcess() bool {
	return os.Getenv(EnvBackground) == "1"
}

// healthClient skips the shared transport pool: this one request is made once,
// and a reused keep-alive would outlive the process that dialed it.
var healthClient = &http.Client{Timeout: 2 * time.Second}

// waitHealthy polls the daemon's /health until it answers, the process dies,
// or StartupTimeout elapses. The process check is what makes a failed bind
// (port already in use) report as an error instead of a hung terminal.
func waitHealthy(url string, pid int) error {
	deadline := time.Now().Add(StartupTimeout)
	target := strings.TrimSuffix(url, "/") + HealthPath

	for {
		if !proc.Alive(pid) {
			return fmt.Errorf("daemon.Start: process %d exited before becoming ready — see %s", pid, LogPath())
		}
		if probeHealthy(target) {
			return nil
		}
		if bindFailureSeen() {
			return fmt.Errorf("daemon.Start: the daemon could not bind its port — see %s", LogPath())
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("daemon.Start: no response from %s within %s — see %s", target, StartupTimeout, LogPath())
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// startupLogScanBytes bounds what one bind-failure poll reads off the end of
// the log. waitHealthy runs this every 150ms for up to StartupTimeout, and
// reading the whole file turned a startup into hundreds of full-file reads
// against a log that is already capped at RunLogMaxBytes and only ever holds
// the lines written since the daemon was spawned.
const startupLogScanBytes int64 = 32 << 10

// bindFailureMarkers are the strings a failed listen leaves behind: Go reports
// "bind: Only one usage of each socket address", while the native accept error
// a second daemon actually hits says "address already in use".
var bindFailureMarkers = [][]byte{
	[]byte("bind: Only one usage of each socket address"),
	[]byte("address already in use"),
}

// bindFailureSeen reports whether the daemon logged an address-in-use failure.
// The server treats a failed bind as fatal, but the log line lands a moment
// before the liveness check notices the exit; catching it keeps a failed start
// from burning the whole startup timeout.
//
// The daemon logs its own exit rather than anything both processes could read
// directly: the failure happens in a goroutine in the child, the log is the
// only channel it has to the parent, and reading a marker out of it is what
// turns that channel into an error message instead of a twenty second hang.
func bindFailureSeen() bool {
	f, err := os.Open(LogPath())
	if err != nil {
		return false
	}
	defer f.Close()

	size, err := logSize()
	if err != nil {
		return false
	}
	offset := size - startupLogScanBytes
	if offset < 0 {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return false
	}
	chunk := make([]byte, size-offset)
	if _, err := io.ReadFull(f, chunk); err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return false
	}
	for _, marker := range bindFailureMarkers {
		if bytes.Contains(chunk, marker) {
			return true
		}
	}
	return false
}

// probeHealthy reports whether /health answers 200.
func probeHealthy(url string) bool {
	resp, err := healthClient.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
