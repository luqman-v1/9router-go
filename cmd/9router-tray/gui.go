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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/getlantern/systray"

	"9router/proxy/internal/tray"
)

//go:embed tray.ico
var trayIcon []byte

// trayBranding carries the two settings the tray renders: the menu/tooltip
// title and the icon it was told to use. Both fall back to the project's own,
// because a tray that shows nothing is worse than one that shows the default.
type trayBranding struct {
	Title string
}

// loadTrayBranding reads trayAppName from the gateway's settings row.
//
// The gateway is the only place the setting lives, and this process may start
// before it, so every failure path is the default title rather than an empty
// one: a missing port, an unreachable gateway, or an unreadable body all mean
// "9Router Go". C5 applies — an unreadable state renders the default, never
// nothing.
//
// The icon is not fetched: tray.ico stays embedded, so a local file or a
// broken URL can never leave the tray with no mark at all.
func loadTrayBranding(port int) trayBranding {
	if port <= 0 {
		return trayBranding{Title: defaultTrayTitle}
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/api/settings", port))
	if err != nil {
		return trayBranding{Title: defaultTrayTitle}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return trayBranding{Title: defaultTrayTitle}
	}
	var settings struct {
		TrayAppName string `json:"trayAppName"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&settings); err != nil {
		return trayBranding{Title: defaultTrayTitle}
	}
	if strings.TrimSpace(settings.TrayAppName) == "" {
		return trayBranding{Title: defaultTrayTitle}
	}
	return trayBranding{Title: settings.TrayAppName}
}

// menu holds the items once they are built. systray has no "menu about to
// open" callback, so the menu is created once and then refreshed in place:
// rebuilding is impossible, hiding the stale truth is not.
type menu struct {
	status    *systray.MenuItem
	start     *systray.MenuItem
	stop      *systray.MenuItem
	autostart *systray.MenuItem
}

// defaultTrayTitle is the tooltip and menu title the tray uses before the
// gateway answers its settings read. trayAppName overrides it; an unreadable
// setting means the default, never an empty title bar.
const defaultTrayTitle = "9Router Go"

func runGUI(actions tray.Actions, port int) error {
	systray.Run(func() {
		systray.SetIcon(trayIcon)
		branding := loadTrayBranding(port)
		systray.SetTitle(branding.Title)
		go runMenu(actions, port, branding.Title)
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
func runMenu(actions tray.Actions, port int, title string) {
	m := &menu{
		status: systray.AddMenuItem(title, ""),
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
