package service

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrNoSupervisor reports that the installed backend has no runtime
// supervisor: cron only wakes the gateway at boot, so process control falls
// back to the pid-file daemon (`9router-go start|stop|restart`).
var ErrNoSupervisor = errors.New("service: autostart backend has no supervisor, manage the process directly")

// controlTimeout bounds how long start/stop/restart wait for the supervised
// service to reach its target state.
const controlTimeout = 10 * time.Second

// Start starts the installed autostart entry's service right now without
// changing its boot enablement.
func Start() (Info, error) {
	return control("start")
}

// Stop stops the installed service right now; it stays enabled for boot.
func Stop() (Info, error) {
	return control("stop")
}

// Restart restarts the installed service right now.
func Restart() (Info, error) {
	return control("restart")
}

// control dispatches a lifecycle operation to whichever backend owns the
// installed entry, then re-probes it so the caller reports fresh state.
func control(op string) (Info, error) {
	info, ok := Status()
	if !ok {
		return Info{}, ErrNotInstalled
	}
	var err error
	switch info.Backend {
	case BackendSystemdUser:
		err = controlSystemd(op)
	case BackendLaunchd:
		err = controlLaunchd(op)
	case BackendOpenRC:
		err = controlOpenRC(op)
	case BackendRunit:
		err = controlRunit(op)
	case BackendWindowsTask:
		err = controlWindowsTask(op)
	case BackendWindowsStartup:
		// The Startup folder only fires at logon (like cron @reboot): there is
		// no supervisor to start/stop/restart, so control falls back to the
		// pid-file daemon.
		return Info{}, ErrNoSupervisor
	case BackendCron:
		return Info{}, ErrNoSupervisor
	default:
		return Info{}, ErrNotInstalled
	}
	if err != nil {
		return Info{}, err
	}
	if refreshed, ok := Status(); ok {
		return refreshed, nil
	}
	return info, nil
}

func controlSystemd(op string) error {
	if _, err := systemctl(op, unitName); err != nil {
		return err
	}
	switch op {
	case "start", "restart":
		if !waitSystemdActive() {
			return fmt.Errorf("service: %s did not become active after %s (logs: %s)", unitName, op, logHint())
		}
	case "stop":
		if !waitSystemdInactive() {
			return fmt.Errorf("service: %s did not stop after %s", unitName, op)
		}
	default:
		return fmt.Errorf("service: unknown operation %q", op)
	}
	return nil
}

// logHint points at the user journal, the only scope this package manages.
func logHint() string {
	return "journalctl --user -u 9router-go.service"
}

// waitSystemdInactive polls until the unit leaves the active state.
func waitSystemdInactive() bool {
	deadline := time.Now().Add(controlTimeout)
	for {
		if !systemctlActive() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func controlLaunchd(op string) error {
	path := launchdPlistPath()
	if path == "" {
		return fmt.Errorf("service: cannot resolve home directory for launchd agent")
	}
	switch op {
	case "start":
		if out, err := run("launchctl", "load", "-w", path); err != nil {
			// Already loaded: kick the job instead of failing the start.
			if !strings.Contains(out, "already loaded") && !strings.Contains(out, "already bootstrapped") {
				return err
			}
			_, err = run("launchctl", "start", launchdLabel)
			return err
		}
		return nil
	case "stop":
		// Unload without -w: stops the job now but keeps it enabled for the
		// next login.
		_, err := run("launchctl", "unload", path)
		return err
	case "restart":
		_, _ = run("launchctl", "unload", path)
		_, err := run("launchctl", "load", "-w", path)
		return err
	default:
		return fmt.Errorf("service: unknown operation %q", op)
	}
}

func controlOpenRC(op string) error {
	_, err := run("rc-service", "9router-go", op)
	return err
}

func controlRunit(op string) error {
	_, err := run("sv", op, runitTarget())
	return err
}

func controlWindowsTask(op string) error {
	switch op {
	case "start":
		_, err := run("schtasks.exe", "/run", "/tn", windowsTaskName)
		return err
	case "stop":
		_, err := run("schtasks.exe", "/end", "/tn", windowsTaskName)
		return err
	case "restart":
		_, _ = run("schtasks.exe", "/end", "/tn", windowsTaskName)
		_, err := run("schtasks.exe", "/run", "/tn", windowsTaskName)
		return err
	default:
		return fmt.Errorf("service: unknown operation %q", op)
	}
}
