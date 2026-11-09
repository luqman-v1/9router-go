// Command 9router-tray parks a system-tray icon that observes and controls the
// 9router-go gateway.
//
// It is a second binary on purpose. A GUI toolkit cannot be linked into
// 9router-go without breaking the CGO_ENABLED=0 cross-build for all five
// release targets, so the tray lives outside the core and the core never learns
// it exists (docs/specs/tray-companion-architecture.md §X2).
//
// This binary cgo-builds per platform and is NOT cross-compiled from the
// release loop. That is the whole point of keeping it out of CROSS_TARGETS:
// Windows needs no cgo (getlantern/systray uses the Win32 API directly), while
// Linux and macOS do, so each platform builds its own artifact on its own host.
//
// The rules this binary must not break:
//
//	C2 — port + PID + data scope verified before any control action
//	C3 — the CLI token never leaves this process without socket-owner proof
//	C4 — Quit releases the icon; it does not stop the gateway
//	C5 — an unreadable state renders Unknown, never Stopped
//	C6 — no credential in argv, env, logs, menu text, or errors
package main

import (
	"fmt"
	"os"

	"github.com/urfave/cli/v2"

	"9router/proxy/internal/config"
	"9router/proxy/internal/tray"
)

func main() {
	app := &cli.App{
		Name:  "9router-tray",
		Usage: "System-tray companion for the 9router-go gateway",
		Flags: []cli.Flag{
			&cli.IntFlag{
				Name:  "port",
				Usage: "gateway port to observe (defaults to the configured PORT)",
			},
		},
		Action: runTray,
	}
	if err := app.Run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runTray(cCtx *cli.Context) error {
	port := cCtx.Int("port")
	if port <= 0 {
		port = config.LoadConfig().Port
	}

	actions := tray.Actions{
		Snapshot:         func() tray.Snapshot { return tray.Resolve(tray.Gather(port), port) },
		Start:            startGateway,
		StopSupervised:   stopSupervised,
		StopVerified:     stopVerified,
		AutostartEnabled: autostartEnabled,
		SetAutostart:     setAutostart,
	}

	return runGUI(actions, port)
}
