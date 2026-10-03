package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/urfave/cli/v2"
	"go.uber.org/fx"

	"9router/proxy/internal/app"
	"9router/proxy/internal/daemon"
	"9router/proxy/internal/shutdown"
	"9router/proxy/internal/updater"
)

func main() {
	app := &cli.App{
		Name:  "9router-go",
		Usage: "AI API proxy gateway with token saver features",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  "rtk",
				Value: os.Getenv("RTK_ENABLED") != "false",
				Usage: "enable RTK input compression (env: RTK_ENABLED)",
			},
			&cli.BoolFlag{
				Name:  "caveman",
				Value: os.Getenv("CAVEMAN_ENABLED") == "true",
				Usage: "enable Caveman terse output style (env: CAVEMAN_ENABLED)",
			},
			&cli.BoolFlag{
				Name:  "ponytail",
				Value: os.Getenv("PONYTAIL_ENABLED") == "true",
				Usage: "enable Ponytail lazy dev code style (env: PONYTAIL_ENABLED)",
			},
			&cli.BoolFlag{
				Name:  "adhd",
				Value: os.Getenv("ADHD_ENABLED") == "true",
				Usage: "enable I have ADHD action-first output style (env: ADHD_ENABLED)",
			},
			&cli.BoolFlag{
				Name:  "auto-update",
				Value: os.Getenv("AUTO_UPDATE") == "true",
				Usage: "automatically download and install updates if available (env: AUTO_UPDATE)",
			},
			&cli.BoolFlag{
				Name:  "no-injection-guard",
				Value: os.Getenv("INJECTION_GUARD_DISABLED") == "true",
				Usage: "disable the prompt-injection detector (on by default; env: INJECTION_GUARD_DISABLED)",
			},
			&cli.BoolFlag{
				Name:    daemon.BackgroundName,
				Aliases: []string{"d"},
				Usage:   "run as a background daemon: detaches from the terminal and returns (env: 9ROUTER_BACKGROUND)",
			},
		},
		Commands: []*cli.Command{
			{
				Name:  "version",
				Usage: "Display version details and check for updates",
				Action: func(cCtx *cli.Context) error {
					info, err := updater.CheckUpdate(cCtx.Context)
					if err != nil {
						fmt.Printf("9router-go version %s (%s/%s)\nUpdate check failed: %v\n", updater.CurrentVersion, runtime.GOOS, runtime.GOARCH, err)
						return nil
					}
					fmt.Printf("9router-go version %s (%s/%s)\n", info.CurrentVersion, info.OS, info.Arch)
					fmt.Printf("Latest version: %s\n", info.LatestVersion)
					if info.HasUpdate {
						fmt.Printf("\n🚀 NEW UPDATE AVAILABLE! (%s)\nNotes: %s\nRun '9router-go update' to install.\n", info.LatestVersion, info.ReleaseNotes)
					} else {
						fmt.Println("App is up to date.")
					}
					return nil
				},
			},
			{
				Name:  "update",
				Usage: "Check and perform self-update to the latest version",
				Action: func(cCtx *cli.Context) error {
					fmt.Printf("Checking for updates (current: %s)...\n", updater.CurrentVersion)
					info, err := updater.CheckUpdate(cCtx.Context)
					if err != nil {
						return fmt.Errorf("update check failed: %w", err)
					}
					if !info.HasUpdate {
						fmt.Printf("9router-go is already on the latest version (%s).\n", info.CurrentVersion)
						return nil
					}
					fmt.Printf("Downloading update v%s...\n", info.LatestVersion)
					if err := updater.PerformSelfUpdate(info.DownloadURL, info.SHA256); err != nil {
						return fmt.Errorf("update failed: %w", err)
					}
					fmt.Println("✅ 9router-go updated successfully!")
					return nil
				},
			},
			{
				Name:  "mitm",
				Usage: "Manage MITM proxy for CLI tool traffic interception",
				Subcommands: []*cli.Command{
					{
						Name:   "enable",
						Usage:  "Start MITM proxy (DNS redirect + TLS intercept on :443)",
						Action: mitmEnable,
					},
					{
						Name:   "disable",
						Usage:  "Stop MITM proxy and remove DNS entries",
						Action: mitmDisable,
					},
					{
						Name:   "status",
						Usage:  "Show MITM proxy status",
						Action: mitmStatus,
					},
				},
			},
			{
				Name:   "start",
				Usage:  "Start the gateway in the background and return (same as --background)",
				Action: startDetached,
			},
			{
				Name:   "stop",
				Usage:  "Stop the background gateway",
				Action: stopDetached,
			},
			{
				Name:   "restart",
				Usage:  "Restart the background gateway",
				Action: restartDetached,
			},
			{
				Name:   "status",
				Usage:  "Show whether the background gateway is running",
				Action: statusDetached,
			},
			{
				Name:  "logs",
				Usage: "Print the tail of the background run log",
				Flags: []cli.Flag{
					&cli.IntFlag{Name: "lines", Aliases: []string{"n"}, Value: 40, Usage: "number of log lines to print"},
				},
				Action: logsDetached,
			},
		},
		Action: foregroundOrBackground,
	}
	if err := app.Run(os.Args); err != nil {
		log.Fatal(err)
	}
}

// foregroundOrBackground is the flag-free entry point: with --background (or
// -d, or the `start` sub-command) it detaches and returns, otherwise it runs
// the server in this terminal exactly as before.
func foregroundOrBackground(cCtx *cli.Context) error {
	if cCtx.Bool(daemon.BackgroundName) {
		return startDetached(cCtx)
	}
	return runServer(cCtx)
}

func runServer(cCtx *cli.Context) error {
	if logPath := os.Getenv("LOG_FILE"); logPath != "" {
		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err == nil {
			log.SetOutput(logFile)
			defer logFile.Close()
		} else {
			log.Printf("[config] warning: cannot open LOG_FILE=%s, using stderr: %v", logPath, err)
		}
	}

	cliParams := app.NewCLIParams(cCtx)

	fxApp := fx.New(
		app.AppModule,
		fx.Replace(cliParams),
		app.DefaultFxLogger(),
	)

	startCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := fxApp.Start(startCtx); err != nil {
		return err
	}

	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)

	// A daemon records its PID so `9router-go stop|status|restart` can find it
	// after the launching terminal is long gone.
	if daemon.IsBackgroundProcess() {
		daemon.RegisterPID()
		defer daemon.UnregisterPID()
	}

	select {
	case <-signals: // ^C or a supervisor's SIGTERM
	case <-shutdown.StopRequested():
		// The dashboard shutdown button and the updater's restart hook both
		// land here. Windows cannot raise SIGTERM on itself, so this request
		// channel is the only portable way to stop this process.
	}

	// A second signal force-quits immediately (e.g. a stream stuck mid-drain).
	go func() {
		<-signals
		fmt.Fprintln(os.Stdout, "\n  Force quitting...")
		os.Exit(1)
	}()

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer stopCancel()
	stopErr := fxApp.Stop(stopCtx)

	// The listener is closed now, so a restart hook can bind the same port.
	shutdown.RunAfterStop()
	return stopErr
}
