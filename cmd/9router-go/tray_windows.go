//go:build windows

package main

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/getlantern/systray"
	"github.com/urfave/cli/v2"

	"9router/proxy/internal/daemon"
)

//go:embed tray.ico
var trayIcon []byte

// runTray starts the daemon and parks a taskbar icon so the terminal is free,
// matching the Node CLI's "Hide to Tray". It blocks until the icon quits.
func runTray(cCtx *cli.Context) error {
	url := daemonURL()
	if _, err := daemon.Start(url, forwardArgs(cCtx)); err != nil && err != daemon.ErrAlreadyRunning {
		return err
	}
	systray.Run(func() {
		systray.SetIcon(trayIcon)
		systray.SetTitle("9router-go")
		systray.SetTooltip("9router-go — " + url)
		open := systray.AddMenuItem("Open dashboard", url+"/dashboard")
		stop := systray.AddMenuItem("Stop server & quit", "")
		go func() {
			for {
				select {
				case <-open.ClickedCh:
					_ = openInBrowser(context.Background(), url+"/dashboard")
				case <-stop.ClickedCh:
					if _, err := daemon.Stop(); err == nil {
						fmt.Println("9router-go stopped from tray.")
					}
					systray.Quit()
				}
			}
		}()
	}, func() {})
	return nil
}
