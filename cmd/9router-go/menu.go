package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/urfave/cli/v2"

	"9router/proxy/internal/config"
	"9router/proxy/internal/daemon"
	"9router/proxy/internal/proc"
	"9router/proxy/internal/updater"
)

// stdinIsTerminal reports whether the bare invocation has a human at the
// keyboard. A service unit, a container, or a pipe runs the same command with
// no TTY, and those must keep booting the server exactly as before.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// runMenu is the bare-`9router-go` entry point on a terminal: the same
// interface picker the Node CLI shows instead of silently booting a server.
func runMenu(cCtx *cli.Context) error {
	in := bufio.NewReader(os.Stdin)
	first := true
	for {
		fmt.Print(menuText())
		line, ok := readChoice(in)
		if !ok {
			// The read ended at EOF before a byte arrived, so there is nobody
			// behind it however the stdin stat looked. /dev/null and a closed
			// pipe are both character devices, which is why stdinIsTerminal
			// let us in — and why a supervisor or the e2e harness, which give
			// the process exactly that, ended up here. That is the
			// non-interactive case, so boot the server as it always did rather
			// than exiting 1 with a menu nobody read.
			if first {
				return runServer(cCtx)
			}
			return nil
		}
		first = false
		choice := parseChoice(line)
		switch choice {
		case "1":
			return openDashboard(cCtx)
		case "2":
			return startDetached(cCtx)
		case "3":
			return runServer(cCtx)
		case "4":
			if err := stopDetached(cCtx); err != nil {
				return err
			}
		case "5":
			if err := statusDetached(cCtx); err != nil {
				return err
			}
		case "6":
			return startTray()
		default:
			return nil
		}
	}
}

// menuText renders the header plus the numbered items, with the live daemon
// state in front of the choice so the pick is informed.
func menuText() string {
	state := "stopped"
	if pid := daemon.RunningPID(); pid != 0 {
		state = fmt.Sprintf("\x1b[32mrunning (pid %d)\x1b[0m", pid)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n  %s v%s (%s/%s)\n", binaryName(), updater.CurrentVersion, runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&b, "  Server: %s  [%s]\n\n", daemonURL(), state)
	b.WriteString("  1) Open dashboard (Web UI)\n")
	b.WriteString("  2) Start in background\n")
	b.WriteString("  3) Start in this terminal (foreground)\n")
	b.WriteString("  4) Stop background server\n")
	b.WriteString("  5) Show status\n")
	b.WriteString("  6) Run in background with tray icon (frees the terminal)\n")
	b.WriteString("  0) Exit\n\n")
	b.WriteString("  Choose [1]: ")
	return b.String()
}

// readChoice blocks for one line. ok is false when the stream ended without
// yielding anything: the caller distinguishes "the operator pressed Ctrl+Z" from
// "there was never an operator" by whether this was the first read.
func readChoice(in *bufio.Reader) (line string, ok bool) {
	line, err := in.ReadString('\n')
	if err != nil && err != io.EOF {
		return "0", true
	}
	// io.EOF with a partial line (a terminal that closed mid-line) still
	// carries the operator's input; EOF with nothing does not.
	return line, line != ""
}

// parseChoice maps the typed line to a menu key. Enter alone takes the
// default (open dashboard); anything unrecognised exits rather than looping.
func parseChoice(line string) string {
	s := strings.TrimSpace(line)
	if s == "" {
		return "1"
	}
	switch s {
	case "0", "1", "2", "3", "4", "5", "6":
		return s
	case "q", "Q", "exit":
		return "0"
	}
	return "0"
}

// startTray hands the tray to its own binary and returns, so the terminal is
// free while the icon stays.
//
// The tray lives in cmd/9router-tray because a GUI toolkit cannot be linked
// into this binary without breaking the CGO_ENABLED=0 cross-build for all five
// targets (docs/specs/tray-companion-architecture.md §X2). Spawning keeps the
// menu entry the Node CLI has ("Hide to Tray") without importing it here.
//
// Only the sibling binary is tried: a PATH lookup could pick up a different
// install's tray, which would then talk to a gateway that is not this one.
func startTray() error {
	self, err := proc.SelfExecutable()
	if err != nil {
		return fmt.Errorf("tray: locate this binary: %w", err)
	}
	exe := filepath.Join(filepath.Dir(self), trayBinaryName())
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if _, err := os.Stat(exe); err != nil {
		return fmt.Errorf("tray: %s not found next to %s — install the 9router-tray package", filepath.Base(exe), filepath.Base(self))
	}
	// CommandContext with a context that is never cancelled, not a deadline:
	// this process is released immediately below and is meant to outlive the
	// terminal, so a deadline here would kill the tray it just started.
	cmd := exec.CommandContext(context.Background(), exe, "--port", strconv.Itoa(config.LoadConfig().Port))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("tray: start %s: %w", filepath.Base(exe), err)
	}
	// Releasing lets the tray outlive this terminal, the same reason the
	// background daemon is released rather than waited on.
	_ = cmd.Process.Release()
	fmt.Printf("Tray started. Right-click the icon to open the dashboard or quit.\n")
	return nil
}

// trayBinaryName follows the same rename rule as binaryName, so an
// `opencode-go` install looks for `opencode-go-tray`.
func trayBinaryName() string {
	return binaryName() + "-tray"
}

// openDashboard makes sure the daemon is up, then hands the dashboard URL to
// the platform browser.
func openDashboard(cCtx *cli.Context) error {
	url := daemonURL()
	target := url + "/dashboard"
	if daemon.RunningPID() == 0 {
		res, err := daemon.Start(url, forwardArgs(cCtx))
		if err != nil && !errors.Is(err, daemon.ErrAlreadyRunning) {
			return err
		}
		if res.URL != "" {
			target = res.URL
		}
	}
	fmt.Printf("Opening %s\n", target)
	return openInBrowser(cCtx.Context, target)
}

// openInBrowser uses the OS opener so no dependency is pulled in.
//
// CommandContext rather than Command: the linters require a context, and it
// costs nothing here — the opener is detached from the caller and the process
// outlives the call either way. The dashboard URL is built from the daemon's
// own listen address, never from client input, so it cannot smuggle an
// argument to the opener.
func openInBrowser(ctx context.Context, target string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", target).Start()
	case "darwin":
		return exec.CommandContext(ctx, "open", target).Start()
	default:
		return exec.CommandContext(ctx, "xdg-open", target).Start()
	}
}
