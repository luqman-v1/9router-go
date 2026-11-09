package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"9router/proxy/internal/daemon"
	"9router/proxy/internal/proc"
	"9router/proxy/internal/service"
)

// gatewayBinaryName mirrors cmd/9router-go's rename rule so an `opencode-go`
// install looks for its own sibling rather than a different install's gateway.
func gatewayBinaryName() string {
	if base := filepath.Base(os.Args[0]); len(base) > 5 && base[len(base)-5:] == "-tray" {
		return base[:len(base)-5]
	}
	return "9router-go"
}

// gatewayPath resolves the sibling gateway binary. Only the sibling is tried: a
// PATH lookup could pick up another install whose data scope this tray is not
// observing, and controlling that one would be a stranger (C2).
func gatewayPath() (string, error) {
	self, err := proc.SelfExecutable()
	if err != nil {
		return "", fmt.Errorf("locate this binary: %w", err)
	}
	exe := filepath.Join(filepath.Dir(self), gatewayBinaryName())
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if _, err := os.Stat(exe); err != nil {
		return "", fmt.Errorf("%s not found next to %s", filepath.Base(exe), filepath.Base(self))
	}
	return exe, nil
}

// cliTimeout bounds one gateway CLI call. It has to clear daemon.StartupTimeout
// (20s, the wait for /health) with room to spare: a shorter deadline would
// report a failure for a gateway that is still booting and about to answer.
const cliTimeout = daemon.StartupTimeout + 15*time.Second

// runGatewayCLI shells out to the gateway binary. The tray never builds a
// request of its own, so every identity check that already guards the CLI
// guards the tray too.
//
// The context is not decoration: without it a `start` that hangs holds this
// goroutine forever, and the menu item stays dead for the rest of the session
// because the click channel has exactly one consumer.
func runGatewayCLI(args ...string) error {
	exe, err := gatewayPath()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), cliTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("%s %v: no answer in %s", filepath.Base(exe), args, cliTimeout)
		}
		return fmt.Errorf("%s %v: %w: %s", filepath.Base(exe), args, err, trimOutput(out))
	}
	return nil
}

func trimOutput(b []byte) string {
	const max = 400
	if len(b) > max {
		b = b[:max]
	}
	return string(b)
}

// startGateway boots the gateway through the CLI's daemon path.
func startGateway() error {
	return runGatewayCLI("start", "--background")
}

// stopSupervised stops through the init supervisor. No token is sent: systemd,
// launchd, openrc and runit all deliver a signal the gateway already drains on.
func stopSupervised() error {
	if _, err := service.Stop(); err != nil {
		return err
	}
	return nil
}

// stopVerified is the only path that may reach daemon.Stop(). That function
// re-checks the PID against the executable recorded in the pid file before it
// signals anything, so a recycled PID cannot become a kill against a stranger.
func stopVerified() error {
	if _, err := daemon.Stop(); err != nil {
		return err
	}
	return nil
}

// autostartEnabled reads the supervisor rather than a cached value: the tray
// reopening after a restart must show what is actually installed.
func autostartEnabled() bool {
	info, ok := service.Status()
	return ok && info.Enabled
}

// setAutostart installs or removes the boot entry.
func setAutostart(on bool) error {
	if on {
		_, err := service.Install(nil)
		return err
	}
	_, err := service.Uninstall()
	return err
}
