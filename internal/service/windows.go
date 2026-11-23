package service

import (
	"fmt"
	"os"
	"strings"
)

// windowsTaskName is the scheduled-task name this package owns.
const windowsTaskName = "9router-go"

// windowsTaskUser resolves the account the logon task runs as. It is the
// interactive user that ran install, never SYSTEM: a SYSTEM task reads
// %APPDATA%\9router — a different data directory from the user's, silently.
// It is a seam for tests.
var windowsTaskUser = func() string {
	for _, key := range []string{"USERNAME", "USER"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

// windowsQuoteArg quotes one argv element for the cmd.exe command line
// schtasks stores in its /tr value. strconv.Quote (used for the POSIX targets)
// escapes backslashes, which cmd.exe does not understand: it would see
// "C:\\Program Files\\9router-go.exe" as a path it cannot run. An element
// without spaces or tabs is safe verbatim.
func windowsQuoteArg(arg string) string {
	if arg == "" {
		return `""`
	}
	if !strings.ContainsAny(arg, " \t") {
		return arg
	}
	return `"` + arg + `"`
}

// windowsCommandLine joins the executable and args into the single command
// line schtasks /tr expects, using cmd.exe quoting rather than POSIX shell
// quoting. It is deliberately separate from commandLine, which stays correct
// for the sh, cron, OpenRC, runit, and systemd targets.
func windowsCommandLine(exe string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, windowsQuoteArg(exe))
	for _, arg := range args {
		parts = append(parts, windowsQuoteArg(arg))
	}
	return strings.Join(parts, " ")
}

func installWindowsTask(exe string, args []string) (Info, error) {
	if goos != "windows" {
		return Info{}, fmt.Errorf("service.Install: windows-task backend only runs on windows")
	}
	// Run `serve` explicitly so an `/it` interactive-logon console session
	// starts the gateway instead of blocking on the interactive CLI menu.
	taskCmd := windowsCommandLine(exe, appendSubcommand(args, "serve"))
	// /it sets the task's logon type to InteractiveToken: it runs only while
	// the run-as user is logged on and stores no password, so schtasks does not
	// stop to prompt for one.
	create := []string{"/create", "/tn", windowsTaskName, "/tr", taskCmd, "/sc", "onlogon", "/it"}
	// /ru names the run-as user explicitly; it defaults to the creating user
	// when it cannot be resolved. Making a scheduled task needs an
	// Administrator console, so install surfaces that from the command result.
	if user := windowsTaskUser(); user != "" {
		create = append(create, "/ru", user)
	}
	create = append(create, "/f")
	if _, err := run("schtasks.exe", create...); err != nil {
		return Info{Backend: BackendWindowsTask, Exec: taskCmd}, err
	}
	if _, err := run("schtasks.exe", "/run", "/tn", windowsTaskName); err != nil {
		return Info{Backend: BackendWindowsTask, Path: windowsTaskName, Exec: taskCmd, Enabled: true}, nil
	}
	return Info{Backend: BackendWindowsTask, Path: windowsTaskName, Exec: taskCmd, Enabled: true, Active: true}, nil
}

func uninstallWindowsTask() (Info, bool) {
	if goos != "windows" {
		return Info{}, false
	}
	out, err := run("schtasks.exe", "/query", "/tn", windowsTaskName)
	if err != nil || !strings.Contains(out, windowsTaskName) {
		return Info{}, false
	}
	_, _ = run("schtasks.exe", "/end", "/tn", windowsTaskName)
	_, _ = run("schtasks.exe", "/delete", "/tn", windowsTaskName, "/f")
	return Info{Backend: BackendWindowsTask, Path: windowsTaskName}, true
}

func statusWindowsTask() (Info, bool) {
	if goos != "windows" {
		return Info{}, false
	}
	out, err := run("schtasks.exe", "/query", "/tn", windowsTaskName)
	if err != nil || !strings.Contains(out, windowsTaskName) {
		return Info{}, false
	}
	active, known := windowsTaskRunning(windowsTaskName)
	return Info{
		Backend:       BackendWindowsTask,
		Path:          windowsTaskName,
		Enabled:       true,
		Active:        active,
		ActiveUnknown: !known,
	}, true
}

// windowsTaskRunning states whether the task is executing right now. It asks
// PowerShell for the TaskState as a number instead of matching "Running" in
// schtasks output, which is localised: on a non-English Windows that literal
// never appears, so `service status` reported Active: false for a task that
// was running. Casting the enum to int is itself locale-independent — the
// enum's ordinal values (Running=4) are fixed by the CIM type, not by the UI
// language, and PS 5.1 ships on every supported Windows.
//
// known is false when the probe itself could not answer — PowerShell missing,
// or blocked by execution policy. The caller then reports an unknown active
// state rather than the false that an absent probe used to imply.
func windowsTaskRunning(name string) (active, known bool) {
	quoted := strings.ReplaceAll(name, "'", "''")
	query := "(Get-ScheduledTask -TaskName '" + quoted + "' -ErrorAction SilentlyContinue).State"
	script := "$s = " + query + "; if ($null -eq $s) { '' } else { [int]$s }"
	out, err := run("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	if err != nil {
		return false, false
	}
	state := strings.TrimSpace(out)
	if state == "" {
		// The task vanished between the schtasks query and this probe.
		return false, false
	}
	// TaskState.Running is ordinal 4 in the CIM ScheduledTask type.
	return state == "4", true
}
