// Package service installs the gateway as an autostart service so it comes
// back after a reboot: a systemd user unit on systemd Linux, a launchd agent
// on macOS, an OpenRC/runit service where those inits run, a cron @reboot
// entry everywhere else, and on Windows a logon task, falling back to the
// per-user Startup folder when creating the task needs an elevated console.
package service

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"9router/proxy/internal/proc"
)

// Backend names the init/autostart system an install targets.
type Backend string

const (
	BackendNone        Backend = "none"
	BackendSystemdUser Backend = "systemd-user"
	BackendLaunchd     Backend = "launchd"
	BackendOpenRC      Backend = "openrc"
	BackendRunit       Backend = "runit"
	BackendCron           Backend = "cron"
	BackendWindowsTask    Backend = "windows-task"
	BackendWindowsStartup Backend = "windows-startup"
	BackendOverrideEnv            = "9ROUTER_SERVICE_BACKEND"
)

// ErrNotInstalled is returned when no autostart entry exists to remove.
var ErrNotInstalled = errors.New("9router-go autostart is not installed")

// Test seams. CI only runs one OS, so the live host facts detection and the
// Windows guard depend on are read through these instead of the runtime
// directly; tests swap them to drive every OS, privilege, and init branch from
// a single runner. They are package-private and never exported.
var (
	goos = runtime.GOOS
	// userHomeDir resolves the home directory the user-scoped backends write
	// to. It is a seam, not a HOME lookup: os.UserHomeDir reads %USERPROFILE%
	// on Windows, so a test that only overrode HOME would resolve to the
	// runner's real home and write the systemd unit, launchd plist, and
	// autostart desktop entry there.
	userHomeDir = os.UserHomeDir
	geteuid     = os.Geteuid
	lookPath    = func(name string) string {
		path, err := exec.LookPath(name)
		if err != nil {
			return ""
		}
		return path
	}
	isDir = func(path string) bool {
		st, err := os.Stat(path)
		return err == nil && st.IsDir()
	}
	// symlink and remove back the two filesystem calls the autostart entry
	// lifecycle needs. They are seams for the same reason as the rest of this
	// block: creating a symlink on Windows requires SeCreateSymbolicLinkPrivilege,
	// which an unelevated runner does not hold, and a removal that cannot
	// happen is exactly the case the uninstall paths must report honestly.
	symlink = os.Symlink
	remove  = os.Remove
)

// Info describes an installed (or just-installed) autostart entry.
type Info struct {
	Backend Backend
	Path    string
	Exec    string
	Enabled bool
	Active  bool
	// ActiveUnknown is set when the backend could not be interrogated, so
	// Active carries no information. Printing the zero value as "not running"
	// would state as fact what is only an unanswered question, which is how the
	// Windows task status stayed wrong on a non-English locale.
	ActiveUnknown bool
}

// Install writes the autostart entry for this machine's init system and
// enables it. extraArgs are baked into the supervised command line (the same
// --flag=value pairs the background daemon forwards to its child).
func Install(extraArgs []string) (Info, error) {
	exe, err := proc.SelfExecutable()
	if err != nil {
		return Info{}, fmt.Errorf("service.Install: locate binary: %w", err)
	}
	switch Detect() {
	case BackendSystemdUser:
		return installSystemd(exe, extraArgs)
	case BackendLaunchd:
		return installLaunchd(exe, extraArgs)
	case BackendOpenRC:
		return installOpenRC(exe, extraArgs)
	case BackendRunit:
		return installRunit(exe, extraArgs)
	case BackendCron:
		return installCron(exe, extraArgs)
	case BackendWindowsTask:
		info, err := installWindowsTask(exe, extraArgs)
		if err == nil {
			return info, nil
		}
		// Creating a logon task needs an Administrator console (schtasks
		// answers "Access is denied" otherwise). The per-user Startup folder
		// runs the same command at logon and needs no elevation, so a denied
		// task falls back to it rather than reporting the install as failed.
		return installWindowsStartup(exe, extraArgs)
	case BackendWindowsStartup:
		return installWindowsStartup(exe, extraArgs)
	default:
		return Info{}, fmt.Errorf("service.Install: no supported autostart backend found on %s", goos)
	}
}

// Uninstall removes every autostart entry this package could have created,
// whichever backend owns it. It reports the entries it removed.
func Uninstall() ([]Info, error) {
	removed := []Info{}
	if info, ok := uninstallSystemd(); ok {
		removed = append(removed, info)
	}
	if info, ok := uninstallLaunchd(); ok {
		removed = append(removed, info)
	}
	if info, ok := uninstallOpenRC(); ok {
		removed = append(removed, info)
	}
	if info, ok := uninstallRunit(); ok {
		removed = append(removed, info)
	}
	if info, ok := uninstallCron(); ok {
		removed = append(removed, info)
	}
	if info, ok := uninstallWindowsTask(); ok {
		removed = append(removed, info)
	}
	if info, ok := uninstallWindowsStartup(); ok {
		removed = append(removed, info)
	}
	if len(removed) == 0 {
		return nil, ErrNotInstalled
	}
	return removed, nil
}

// Status reports the autostart entry in effect, or false when none exists.
func Status() (Info, bool) {
	for _, probe := range []func() (Info, bool){
		statusSystemd,
		statusLaunchd,
		statusOpenRC,
		statusRunit,
		statusCron,
		statusWindowsTask,
		statusWindowsStartup,
	} {
		if info, ok := probe(); ok {
			return info, true
		}
	}
	return Info{}, false
}

// Detect picks the backend Install will use on this machine. The
// 9ROUTER_SERVICE_BACKEND env var forces one (systemd, launchd, openrc,
// runit, cron) for machines with several inits or for testing. The override is
// read before the OS short-circuits so it is authoritative on every platform:
// a pinned backend must win even on Windows, where the schtasks check would
// otherwise return before the env var is ever consulted.
func Detect() Backend {
	if forced := strings.TrimSpace(os.Getenv(BackendOverrideEnv)); forced != "" {
		switch Backend(forced) {
		case BackendSystemdUser, BackendLaunchd, BackendOpenRC, BackendRunit, BackendCron, BackendWindowsStartup:
			return Backend(forced)
		case "systemd", "system", "systemd-system":
			// This package only manages the unprivileged per-user unit, so
			// every systemd alias (including the retired "systemd-system")
			// resolves to it. There is no root-owned scope to opt into.
			return BackendSystemdUser
		}
	}
	if goos == "windows" {
		if lookPath("schtasks.exe") != "" {
			return BackendWindowsTask
		}
		return BackendNone
	}
	if goos == "darwin" {
		return BackendLaunchd
	}
	if goos != "linux" {
		if hasCron() {
			return BackendCron
		}
		return BackendNone
	}
	switch {
	case hasSystemd():
		return BackendSystemdUser
	case hasOpenRC():
		// OpenRC is detected whenever it is the host's init; it has no
		// per-user scope, so installOpenRC refuses without root instead of
		// substituting another backend.
		return BackendOpenRC
	case hasRunit():
		return BackendRunit
	case hasCron():
		return BackendCron
	default:
		return BackendNone
	}
}

// hasSystemd reports whether this Linux host is actually supervised by
// systemd. The /run/systemd/system directory is the authority: a stray
// systemctl shim elsewhere must not select this backend.
func hasSystemd() bool {
	if lookPath("systemctl") == "" {
		return false
	}
	return isDir("/run/systemd/system")
}

func hasOpenRC() bool {
	return lookPath("openrc-run") != "" || lookPath("rc-service") != ""
}

func hasRunit() bool {
	if lookPath("sv") != "" {
		return true
	}
	return isDir("/run/runit")
}

func hasCron() bool {
	return lookPath("crontab") != ""
}

// run runs a helper command, returning its combined output for diagnostics.
// It is a var so tests can observe and stub supervisor calls without touching
// the real init system.
var run = func(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("service: %s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// commandLine joins the executable and args for display and for unit files
// that take a plain command line.
func commandLine(exe string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, quoteArg(exe))
	for _, a := range args {
		parts = append(parts, quoteArg(a))
	}
	return strings.Join(parts, " ")
}

func quoteArg(arg string) string {
	if arg == "" {
		return "''"
	}
	if !strings.ContainsAny(arg, " \t\n\"'\\") {
		return arg
	}
	return strconv.Quote(arg)
}
