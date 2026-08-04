package headroom

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"9router/proxy/internal/constants"
)

// StartupTimeout is how long start waits for the proxy to stay up.
const StartupTimeout = 8 * time.Second

var (
	ErrNotInstalled    = errors.New("Headroom CLI not installed")
	ErrNoPython        = errors.New("Python >= 3.10 not found")
	ErrInvalidExtras   = errors.New("No valid extras to remove")
	ErrInstallFailed   = errors.New("pip install exited with an error")
	ErrUninstallFailed = errors.New("pip uninstall exited with an error")
	ErrStopFailed      = errors.New("Failed to stop headroom proxy")
	ErrEarlyExit       = errors.New("headroom proxy exited during startup")
)

// CodeOf maps a headroom error to the dashboard's error-code string.
func CodeOf(err error) string {
	switch {
	case errors.Is(err, ErrNotInstalled):
		return "NOT_INSTALLED"
	case errors.Is(err, ErrNoPython):
		return "NO_PYTHON"
	case errors.Is(err, ErrInvalidExtras):
		return "INVALID_EXTRAS"
	case errors.Is(err, ErrInstallFailed):
		return "INSTALL_FAILED"
	case errors.Is(err, ErrUninstallFailed):
		return "UNINSTALL_FAILED"
	case errors.Is(err, ErrStopFailed):
		return "STOP_FAILED"
	case errors.Is(err, ErrEarlyExit):
		return "EARLY_EXIT"
	}
	return ""
}

func headroomDir(dataDir string) string {
	return filepath.Join(dataDir, "headroom")
}

func pidFile(dataDir string) string {
	return filepath.Join(headroomDir(dataDir), "proxy.pid")
}

func readPid(dataDir string) int {
	data, err := os.ReadFile(pidFile(dataDir))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return pid
}

func writePid(dataDir string, pid int) error {
	if err := os.MkdirAll(headroomDir(dataDir), constants.FilePermDir); err != nil {
		return err
	}
	return os.WriteFile(pidFile(dataDir), []byte(strconv.Itoa(pid)), constants.FilePermFile)
}

func clearPid(dataDir string) {
	os.Remove(pidFile(dataDir))
}

// GetManagedPid returns the PID from the pid file if it is still alive.
func GetManagedPid(dataDir string) int {
	pid := readPid(dataDir)
	if pid > 0 && pidAlive(pid) {
		return pid
	}
	return 0
}

// StartResult reports the outcome of starting the proxy.
type StartResult struct {
	PID            int  `json:"pid"`
	AlreadyRunning bool `json:"alreadyRunning"`
}

// StopResult reports the outcome of stopping the proxy.
type StopResult struct {
	Stopped bool   `json:"stopped"`
	Reason  string `json:"reason,omitempty"`
	PID     int    `json:"pid,omitempty"`
}

// StartHeadroomProxy spawns `headroom proxy` detached with stdio → proxy.log,
// writes its PID, then waits up to 8s for it to stay up (early exit = failure).
func StartHeadroomProxy(dataDir string, port int, codeAware, kompress bool) (StartResult, error) {
	if port <= 0 || port >= 65536 {
		port = DefaultPort
	}
	binary := FindHeadroomBinary()
	if binary == "" {
		return StartResult{}, ErrNotInstalled
	}
	if pid := GetManagedPid(dataDir); pid > 0 {
		return StartResult{PID: pid, AlreadyRunning: true}, nil
	}
	if err := os.MkdirAll(headroomDir(dataDir), constants.FilePermDir); err != nil {
		return StartResult{}, fmt.Errorf("create headroom dir: %w", err)
	}
	logFile := filepath.Join(headroomDir(dataDir), "proxy.log")
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, constants.FilePermFile)
	if err != nil {
		return StartResult{}, fmt.Errorf("open proxy.log: %w", err)
	}

	args := []string{"proxy", "--port", strconv.Itoa(port)}
	if codeAware {
		args = append(args, "--code-aware")
	}
	if !kompress {
		args = append(args, "--disable-kompress")
	}

	cmd := exec.Command(binary, args...)
	cmd.Stdout = f
	cmd.Stderr = f
	cmd.SysProcAttr = detachedProcAttr() // detached: survives server restart

	if err := cmd.Start(); err != nil {
		f.Close()
		return StartResult{}, fmt.Errorf("spawn headroom proxy: %w", err)
	}
	f.Close() // parent's copy; child holds its own fd after spawn
	pid := cmd.Process.Pid

	if err := writePid(dataDir, pid); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return StartResult{}, fmt.Errorf("write pid file: %w", err)
	}

	// Wait until the process stays alive briefly (success) or exits fast (failure).
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		clearPid(dataDir)
		code := "0"
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				code = strconv.Itoa(ee.ExitCode())
			} else {
				code = "unknown"
			}
		}
		return StartResult{}, fmt.Errorf("%w: exited early (code=%s) — see proxy.log", ErrEarlyExit, code)
	case <-time.After(StartupTimeout):
		return StartResult{PID: pid, AlreadyRunning: false}, nil
	}
}

// StopHeadroomProxy sends SIGTERM, waits 2s, then SIGKILL if still alive.
func StopHeadroomProxy(dataDir string) (StopResult, error) {
	pid := GetManagedPid(dataDir)
	if pid == 0 {
		return StopResult{Stopped: false, Reason: "not_running"}, nil
	}
	if err := sendSigTerm(pid); err != nil {
		clearPid(dataDir)
		return StopResult{}, fmt.Errorf("%w: %v", ErrStopFailed, err)
	}
	// Give it a moment, then force if still alive.
	time.Sleep(2 * time.Second)
	if pidAlive(pid) {
		sendSigKill(pid)
	}
	clearPid(dataDir)
	return StopResult{Stopped: true, PID: pid}, nil
}

// RestartHeadroomProxy stops the managed proxy (up to ~3s), then starts fresh.
func RestartHeadroomProxy(dataDir string, port int, codeAware, kompress bool) (StartResult, error) {
	if pid := GetManagedPid(dataDir); pid > 0 {
		sendSigTerm(pid)
		// Wait up to ~3s for graceful exit, force-kill if still alive.
		for i := 0; i < 30 && pidAlive(pid); i++ {
			time.Sleep(100 * time.Millisecond)
		}
		if pidAlive(pid) {
			sendSigKill(pid)
			time.Sleep(300 * time.Millisecond)
		}
		clearPid(dataDir)
	}
	return StartHeadroomProxy(dataDir, port, codeAware, kompress)
}

func logTail(path string, maxLines int) string {
	if maxLines <= 0 {
		maxLines = 15
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n")
}

// GetHeadroomLogTail returns the last lines of the proxy log.
func GetHeadroomLogTail(dataDir string, maxLines int) string {
	return logTail(filepath.Join(headroomDir(dataDir), "proxy.log"), maxLines)
}

// GetInstallLogTail returns the last lines of the install/uninstall log.
func GetInstallLogTail(dataDir string, maxLines int) string {
	return logTail(filepath.Join(headroomDir(dataDir), "install.log"), maxLines)
}

func filterValidExtras(extras []string) []string {
	var out []string
	for _, e := range extras {
		for _, valid := range CompressionExtras {
			if e == valid {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

// runPip runs a python -m pip command with stdio → install.log (truncated) and
// returns its exit code.
func runPip(dataDir, python string, args []string) (int, error) {
	if err := os.MkdirAll(headroomDir(dataDir), constants.FilePermDir); err != nil {
		return -1, err
	}
	logFile := filepath.Join(headroomDir(dataDir), "install.log")
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, constants.FilePermFile)
	if err != nil {
		return -1, err
	}
	defer f.Close()
	cmd := exec.Command(python, args...)
	cmd.Stdout = f
	cmd.Stderr = f
	if err := cmd.Start(); err != nil {
		return -1, err
	}
	err = cmd.Wait()
	if err == nil {
		return 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	return -1, err
}

// InstallHeadroomExtras installs/upgrades headroom-ai[proxy,<extras>].
func InstallHeadroomExtras(dataDir string, extras []string) (map[string]any, error) {
	requested := filterValidExtras(extras)
	python := FindPython310()
	if python == "" {
		return nil, ErrNoPython
	}
	if FindHeadroomBinary() == "" {
		return nil, ErrNotInstalled
	}
	// Built from a closed set (CompressionExtras) — no shell interpolation.
	extrasList := append([]string{"proxy"}, requested...)
	spec := "headroom-ai[" + strings.Join(extrasList, ",") + "]"
	code, err := runPip(dataDir, python, []string{"-m", "pip", "install", "--upgrade", spec})
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("%w: exited with code=%d — see headroom/install.log", ErrInstallFailed, code)
	}
	st := GetInstalledHeadroomExtras(python)
	return map[string]any{
		"success":   true,
		"code":      code,
		"spec":      spec,
		"installed": st.Installed,
		"version":   st.Version,
		"extras":    st.Extras,
	}, nil
}

// UninstallHeadroomExtras removes the marker packages of each extra; the base
// headroom-ai/proxy install is never touched.
func UninstallHeadroomExtras(dataDir string, extras []string) (map[string]any, error) {
	requested := filterValidExtras(extras)
	python := FindPython310()
	if python == "" {
		return nil, ErrNoPython
	}
	seen := map[string]bool{}
	for _, e := range requested {
		for _, m := range ExtraMarkers[e] {
			seen[m] = true
		}
	}
	if len(seen) == 0 {
		return nil, ErrInvalidExtras
	}
	remove := make([]string, 0, len(seen))
	for p := range seen {
		remove = append(remove, p)
	}
	sort.Strings(remove) // deterministic order
	code, err := runPip(dataDir, python, append([]string{"-m", "pip", "uninstall", "-y"}, remove...))
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("%w: exited with code=%d — see headroom/install.log", ErrUninstallFailed, code)
	}
	st := GetInstalledHeadroomExtras(python)
	return map[string]any{
		"success":   true,
		"code":      code,
		"removed":   remove,
		"installed": st.Installed,
		"version":   st.Version,
		"extras":    st.Extras,
	}, nil
}
