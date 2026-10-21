package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/urfave/cli/v2"

	"9router/proxy/internal/daemon"
	"9router/proxy/internal/service"
)

// serviceInstall writes the autostart entry so the gateway starts on boot,
// then reports which backend owns it.
func serviceInstall(cCtx *cli.Context) error {
	if backend := cCtx.String("backend"); backend != "" {
		if err := os.Setenv(service.BackendOverrideEnv, backend); err != nil {
			return fmt.Errorf("service install: %w", err)
		}
	}
	info, err := service.Install(forwardArgs(cCtx))
	if err != nil {
		return err
	}
	warnPidConflict()
	fmt.Printf("Autostart installed via %s\n", info.Backend)
	fmt.Printf("  Entry:   %s\n", info.Path)
	fmt.Printf("  Command: %s\n", info.Exec)
	switch {
	case info.ActiveUnknown:
		fmt.Println("  State:   enabled — its current state could not be read on this system.")
	case info.Active:
		fmt.Println("  State:   enabled and running — it will start automatically on boot.")
	case info.Enabled:
		fmt.Println("  State:   enabled — it will start automatically on next boot.")
		fmt.Println("  Note:    the current gateway (if any) keeps running; reboot or run `9router-go service restart` to hand over.")
	}
	if info.Backend == service.BackendSystemdUser {
		fmt.Println("  Tip:     run `loginctl enable-linger $USER` to keep it alive after logout.")
	}
	if hint := inspectHint(info.Backend); hint != "" {
		fmt.Printf("  Inspect: %s\n", hint)
	}
	return nil
}

// serviceUninstall removes every autostart entry this binary manages.
func serviceUninstall(_ *cli.Context) error {
	removed, err := service.Uninstall()
	if err != nil {
		// Nothing was removed: either genuinely not installed, or every
		// removal failed (e.g. a root-owned entry and no permission). Report
		// which one instead of claiming it was never installed.
		if info, ok := service.Status(); ok {
			return fmt.Errorf("failed to remove %s entry %s — check permissions and retry", info.Backend, info.Path)
		}
		fmt.Println("9router-go autostart is not installed.")
		return nil
	}
	for _, info := range removed {
		fmt.Printf("Removed %s entry: %s\n", info.Backend, info.Path)
	}
	fmt.Println("Autostart uninstalled — the gateway will no longer start on boot.")
	return nil
}

// serviceStart starts the supervised service now. With no installed entry —
// or a backend without a supervisor (cron) — it falls back to the pid-file
// background daemon so `service start` works everywhere.
func serviceStart(cCtx *cli.Context) error {
	warnPidConflict()
	info, err := service.Start()
	if errors.Is(err, service.ErrNotInstalled) || errors.Is(err, service.ErrNoSupervisor) {
		return startDetached(cCtx)
	}
	if err != nil {
		return err
	}
	fmt.Printf("9router-go started via %s (%s, starts on boot)\n", info.Backend, activeState(info))
	return nil
}

// serviceStop stops the supervised service now but keeps it enabled for boot.
// Without a supervisor it falls back to stopping the background daemon.
func serviceStop(cCtx *cli.Context) error {
	info, err := service.Stop()
	if errors.Is(err, service.ErrNotInstalled) || errors.Is(err, service.ErrNoSupervisor) {
		return stopDetached(cCtx)
	}
	if err != nil {
		return err
	}
	fmt.Printf("9router-go stopped via %s (still enabled for boot)\n", info.Backend)
	return nil
}

// serviceRestart restarts the supervised service now, or the background
// daemon when no supervisor owns the gateway.
func serviceRestart(cCtx *cli.Context) error {
	info, err := service.Restart()
	if errors.Is(err, service.ErrNotInstalled) || errors.Is(err, service.ErrNoSupervisor) {
		return restartDetached(cCtx)
	}
	if err != nil {
		return err
	}
	fmt.Printf("9router-go restarted via %s (%s)\n", info.Backend, activeState(info))
	return nil
}

// warnPidConflict notes when the pid-file daemon is also alive: two
// supervisors binding the same port means one of them crash-loops, which is
// exactly the `activating (auto-restart)` state users hit after installing
// the unit while `9router-go start` still holds the port.
func warnPidConflict() {
	if pid := daemon.RunningPID(); pid > 0 {
		fmt.Printf("  Warning: background daemon (pid %d) from `9router-go start` is also running — both bind the same port. Keep one: `9router-go stop` or `9router-go service stop`.\n", pid)
	}
}

// serviceStatus reports the autostart entry in effect, if any.
func serviceStatus(_ *cli.Context) error {
	info, ok := service.Status()
	if !ok {
		fmt.Println("9router-go autostart is not installed.")
		fmt.Println("  Install with: 9router-go service install")
		return nil
	}
	fmt.Printf("Autostart backend: %s\n", info.Backend)
	fmt.Printf("  Entry:   %s\n", info.Path)
	if info.Exec != "" {
		fmt.Printf("  Command: %s\n", info.Exec)
	}
	fmt.Printf("  Enabled: %v\n", info.Enabled)
	if info.ActiveUnknown {
		fmt.Println("  Active:  unknown (this backend could not be interrogated)")
	} else {
		fmt.Printf("  Active:  %v\n", info.Active)
	}
	if hint := inspectHint(info.Backend); hint != "" {
		fmt.Printf("  Inspect: %s\n", hint)
	}
	return nil
}

// activeState renders the active flag for one-line output. An unanswered
// probe is reported as unknown rather than as false: start and restart re-probe
// through Status, so they inherit the same uncertainty that `service status`
// states explicitly.
func activeState(info service.Info) string {
	if info.ActiveUnknown {
		return "active: unknown"
	}
	return fmt.Sprintf("active: %v", info.Active)
}

// inspectHint prints the native command that introspects the backend entry.
// The common pitfall is probing a user-scope unit with a system-scope
// systemctl, which reports "could not be found" for a unit that is there.
func inspectHint(backend service.Backend) string {
	switch backend {
	case service.BackendSystemdUser:
		return "systemctl --user status 9router-go.service"
	case service.BackendLaunchd:
		return "launchctl list com.9router.gateway"
	case service.BackendOpenRC:
		return "rc-service 9router-go status"
	case service.BackendRunit:
		return "sv status 9router-go"
	case service.BackendCron:
		return "crontab -l | grep 9router-go"
	case service.BackendWindowsTask:
		return "schtasks /query /tn 9router-go"
	case service.BackendWindowsStartup:
		return "explorer shell:startup"
	default:
		return ""
	}
}
