package tunnel

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// errNoTailscale is returned when no tailscale binary is reachable. It is a
// sentinel so a caller can tell "not installed" apart from "installed but not
// logged in", which need different messages in the dashboard.
var errNoTailscale = errors.New("tailscale binary not found")

// errFunnelNotEnabled means the operator's tailnet has the Funnel feature
// switched off. No amount of retrying fixes that; the console has to say so
// and point at the admin console.
var errFunnelNotEnabled = errors.New("tailscale funnel is not enabled on this tailnet")

// fileExists reports whether a path exists and is a regular file. The IsDir
// check matters because a directory named `tailscale` on the PATH would
// otherwise be reported as an installed binary.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// Tailscale probes are kept here rather than in the dashboard handler so the
// watchdog can reach them without importing the handlers package. The handler
// owns the HTTP surface; this owns the questions the binary answers about the
// daemon, and both must agree or the dashboard shows one state while the
// watchdog acts on another.

var (
	// tailscaleBinFn and tailscaleExec are seams. A watchdog tick that shells
	// out to the operator's real tailscale every 60 seconds is not a health
	// check, it is a background job mutating state nobody asked it to touch.
	tailscaleBinFn  = findTailscaleBin
	tailscaleExecFn = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, bin, args...).CombinedOutput()
	}
)

// tailscaleCandidatePaths covers the install locations the dashboard handler
// already knows about. Duplicated rather than shared because the handler's list
// is part of its own contract and this package must not depend on it.
var tailscaleCandidatePaths = []string{
	"/usr/local/bin/tailscale",
	"/opt/homebrew/bin/tailscale",
	"/Applications/Tailscale.app/Contents/MacOS/Tailscale",
	"/usr/sbin/tailscale",
	"/usr/bin/tailscale",
	"/snap/bin/tailscale",
}

func findTailscaleBin() string {
	if p, err := exec.LookPath("tailscale"); err == nil && p != "" {
		return p
	}
	for _, cand := range tailscaleCandidatePaths {
		if fileExists(cand) {
			return cand
		}
	}
	if runtime.GOOS == "windows" {
		for _, cand := range []string{
			`C:\Program Files\Tailscale\tailscale.exe`,
			`C:\Program Files (x86)\Tailscale\tailscale.exe`,
		} {
			if fileExists(cand) {
				return cand
			}
		}
	}
	return ""
}

// tailscaleStatusJSON is the subset of `tailscale status --json` these probes
// need. BackendState is the one that says whether the daemon is usable; the
// rest would be decoded and ignored.
type tailscaleStatusJSON struct {
	BackendState string `json:"BackendState"`
}

func probeTailscale(ctx context.Context, bin string) (*tailscaleStatusJSON, error) {
	if bin == "" {
		return nil, errNoTailscale
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	out, err := tailscaleExecFn(ctx, bin, "status", "--json")
	if err != nil {
		return nil, err
	}
	var st tailscaleStatusJSON
	if err := json.Unmarshal(out, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// TailscaleDaemonUp reports whether the Tailscale daemon is running and logged
// in. A logged-out daemon is reported as down on purpose: a funnel cannot be
// established against it, and the recovery path would otherwise retry forever
// against a node that needs a human to authenticate.
func TailscaleDaemonUp(ctx context.Context) bool {
	bin := tailscaleBinFn()
	if bin == "" {
		return false
	}
	st, err := probeTailscale(ctx, bin)
	return err == nil && st != nil && st.BackendState == "Running"
}

// TailscaleFunnelUp reports whether the funnel is currently serving. It is
// checked by asking for the funnel's status rather than by probing a URL,
// because the hostname is the operator's and this package must not assume it.
func TailscaleFunnelUp(ctx context.Context) bool {
	bin := tailscaleBinFn()
	if bin == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	out, err := tailscaleExecFn(ctx, bin, "funnel", "status")
	if err != nil {
		return false
	}
	// `funnel status` exits 0 with an empty list when nothing is served, so
	// the output has to be read rather than the exit code trusted.
	return strings.Contains(strings.ToLower(string(out)), "https://")
}

// StartTailscaleFunnel establishes the funnel for port. It mirrors the
// dashboard's enable path: `--bg` so the call returns once the funnel is up
// rather than holding the connection open.
func StartTailscaleFunnel(ctx context.Context, port int) error {
	bin := tailscaleBinFn()
	if bin == "" {
		return errNoTailscale
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	out, err := tailscaleExecFn(ctx, bin, "funnel", "--bg", strconv.Itoa(port))
	if err != nil {
		return &TailscaleError{Output: string(out), Err: err}
	}
	if strings.Contains(string(out), "Funnel is not enabled") {
		return errFunnelNotEnabled
	}
	return nil
}

// TailscaleError carries the CLI's own output, because a bare "exit status 1"
// tells the operator nothing about whether the funnel feature is off, the
// daemon is logged out, or the port is already taken.
type TailscaleError struct {
	Output string
	Err    error
}

func (e *TailscaleError) Error() string {
	if out := strings.TrimSpace(e.Output); out != "" {
		return "tailscale: " + out
	}
	return "tailscale: " + e.Err.Error()
}

func (e *TailscaleError) Unwrap() error { return e.Err }
