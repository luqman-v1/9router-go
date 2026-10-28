//go:build windows || tray

// The build constraint is what keeps `go vet ./...`, `go test ./...` and
// golangci-lint green on a Linux box. getlantern/systray compiles through
// pkg-config for ayatana-appindicator3-0.1 on Linux and through Cocoa on
// macOS, so an unconstrained import would make every workspace command fail on
// a machine that has no GUI toolkit — for a dependency none of those commands
// exercise (docs/specs/tray-companion-architecture.md §X2).
//
// Windows is in the constraint without a tag because systray reaches the Win32
// API directly there: no cgo, no pkg-config, nothing to install. Requiring a
// tag on the one platform that builds out of the box would mean a plain
// `go build ./cmd/9router-tray/` silently produces the stub instead.
package main

import (
	_ "embed"
	"fmt"
	"os/exec"
	"runtime"
	"time"

	"github.com/getlantern/systray"

	"9router/proxy/internal/tray"
)

//go:embed tray.ico
var trayIcon []byte

// menu holds the items once they are built. systray has no "menu about to
// open" callback, so the menu is created once and then refreshed in place:
// rebuilding is impossible, hiding the stale truth is not.
type menu struct {
	status    *systray.MenuItem
	start     *systray.MenuItem
	stop      *systray.MenuItem
	autostart *systray.MenuItem
}

func runGUI(actions tray.Actions, port int) error {
	systray.Run(func() {
		systray.SetIcon(trayIcon)
		systray.SetTitle("9Router Go")
		go runMenu(actions, port)
	}, func() {
		// Quit Tray: release the icon and return. Deliberately empty — C4 says
		// quitting the tray must not touch the gateway, and the daemon has
		// already been released by whoever started it, so there is no handle
		// here to close.
	})
	return nil
}

// runMenu builds the items once, then keeps them current.
//
// The refresh loop is the whole point: a tray that draws its state at startup
// and never again says "Running" forever, which is exactly the lie C5 forbids.
// One second is fast enough that a user who starts the gateway from the CLI
// sees the menu agree before they can open it, and cheap enough that reading a
// pid file plus a non-blocking service probe costs nothing.
func runMenu(actions tray.Actions, port int) {
	m := &menu{
		status: systray.AddMenuItem("9Router Go", ""),
		start:  systray.AddMenuItem("Start Gateway", ""),
		stop:   systray.AddMenuItem("Stop Gateway", ""),
		// A real checkbox rather than a "✓ " prefix: the prefix is a picture of
		// a checkbox that the OS will not toggle, and users click it expecting
		// the check to move. AddMenuItemCheckbox is checkable on Windows and
		// macOS; on Linux it is the same call, and the checked state still
		// renders through Check/Uncheck.
		autostart: systray.AddMenuItemCheckbox("Enable Auto-start", "", false),
	}
	m.status.Disable()

	open := systray.AddMenuItem("Open Dashboard", "")
	quit := systray.AddMenuItem("Quit Tray", "")

	go func() {
		for range open.ClickedCh {
			openDashboard(port)
		}
	}()
	go func() {
		for range m.start.ClickedCh {
			report(actions.Start())
			refresh(m, actions)
		}
	}()
	go func() {
		for range m.stop.ClickedCh {
			report(actions.Stop())
			refresh(m, actions)
		}
	}()
	go func() {
		for range m.autostart.ClickedCh {
			// Read the installed state again rather than trusting the checkbox:
			// the entry may have changed via the CLI since the last refresh.
			// The checkbox is the user's intent, the service is the truth, and
			// when they disagree the service wins and the box snaps back.
			report(actions.SetAutostart(!actions.AutostartEnabled()))
			refresh(m, actions)
		}
	}()
	go func() {
		for range quit.ClickedCh {
			systray.Quit()
			return
		}
	}()

	refresh(m, actions)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		refresh(m, actions)
	}
}

// refresh redraws the items from a fresh snapshot. Every branch is a state the
// user can act from, and nothing is inferred past what the snapshot proved.
func refresh(m *menu, actions tray.Actions) {
	snap := actions.Snapshot()
	systray.SetTooltip(snap.Title())
	m.status.SetTitle(snap.Title())

	// Start is offered only from Stopped. Offering it from Unknown is how the
	// user gets ErrAlreadyRunning for a click they did not cause (C5).
	if snap.CanStart() {
		m.start.Show()
		m.start.Enable()
	} else {
		m.start.Hide()
	}

	// Stop is offered only with a verified PID. Whether it will actually run is
	// DecideStop's question, answered on click — the item stays visible so the
	// refusal can be explained rather than the option silently vanishing.
	if snap.CanStop() {
		m.stop.Show()
		m.stop.Enable()
	} else {
		m.stop.Hide()
	}

	// The checkbox reflects what is installed, not what was last clicked: a
	// failed install leaves the box where the service actually is.
	if actions.AutostartEnabled() {
		m.autostart.Check()
	} else {
		m.autostart.Uncheck()
	}
}

// report surfaces a failure without ever printing a credential (C6): errors
// from the actions carry a command and its output, never a token.
func report(err error) {
	if err == nil {
		return
	}
	fmt.Println(err)
}

func openDashboard(port int) {
	url := fmt.Sprintf("http://127.0.0.1:%d/dashboard", port)
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
