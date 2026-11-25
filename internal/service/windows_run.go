package service

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// windowsRunKeyPath stores current-user Run entries loaded on interactive logon.
// It is a var so Windows tests can redirect it to a throwaway subkey.
var windowsRunKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`

func windowsRunDisplayPath() string {
	return `HKCU\` + windowsRunKeyPath + `\` + windowsTaskName
}

// Package-private seams matching windowsTaskUser and readCrontab/writeCrontab
// so tests drive every Windows branch without mutating HKCU on the host.
var (
	windowsIsElevated = defaultWindowsIsElevated
	windowsRunRead    = defaultWindowsRunRead
	windowsRunWrite   = defaultWindowsRunWrite
	windowsRunDelete  = defaultWindowsRunDelete
)

// installWindowsAuto installs windows-task for elevated sessions and falls back
// to the per-user windows-run registry backend when running unelevated or when
// schtasks.exe /create exits with an execution failure.
func installWindowsAuto(exe string, args []string) (Info, error) {
	if !windowsIsElevated() {
		return installWindowsRun(exe, args)
	}
	info, err := installWindowsTask(exe, args)
	if err == nil {
		return info, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		runInfo, runErr := installWindowsRun(exe, args)
		if runErr == nil {
			return runInfo, nil
		}
		return info, fmt.Errorf("%w (fallback %s failed: %w)", err, BackendWindowsRun, runErr)
	}
	return info, err
}

func installWindowsRun(exe string, args []string) (Info, error) {
	if goos != "windows" {
		return Info{}, fmt.Errorf("service.Install: windows-run backend only runs on windows")
	}
	cmd := windowsCommandLine(exe, appendSubcommand(args, "start"))
	path := windowsRunDisplayPath()
	if err := windowsRunWrite(windowsTaskName, cmd); err != nil {
		return Info{Backend: BackendWindowsRun, Path: path, Exec: cmd}, fmt.Errorf("service: write %s: %w", path, err)
	}
	return Info{Backend: BackendWindowsRun, Path: path, Exec: cmd, Enabled: true}, nil
}

func uninstallWindowsRun() (Info, bool) {
	if goos != "windows" {
		return Info{}, false
	}
	deleted, err := windowsRunDelete(windowsTaskName)
	if err != nil || !deleted {
		return Info{}, false
	}
	return Info{Backend: BackendWindowsRun, Path: windowsRunDisplayPath()}, true
}

func statusWindowsRun() (Info, bool) {
	if goos != "windows" {
		return Info{}, false
	}
	cmd, ok, err := windowsRunRead(windowsTaskName)
	if err != nil || !ok || strings.TrimSpace(cmd) == "" {
		return Info{}, false
	}
	return Info{Backend: BackendWindowsRun, Path: windowsRunDisplayPath(), Exec: cmd, Enabled: true}, true
}
