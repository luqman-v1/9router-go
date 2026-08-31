package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"path/filepath"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/urfave/cli/v2"

	"9router/proxy/internal/config"
	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers"
	"9router/proxy/internal/middleware"
	"9router/proxy/internal/providers"
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
				Name:  "auto-update",
				Value: os.Getenv("AUTO_UPDATE") == "true",
				Usage: "automatically download and install updates if available (env: AUTO_UPDATE)",
			},
			&cli.BoolFlag{
				Name:  "no-injection-guard",
				Value: os.Getenv("INJECTION_GUARD_DISABLED") == "true",
				Usage: "disable the prompt-injection detector (on by default; env: INJECTION_GUARD_DISABLED)",
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
		},
		Action: runServer,
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatal(err)
	}
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

	cfg := config.LoadConfig()

	if err := db.InitGlobalDatabase(cfg.DatabasePath); err != nil {
		return fmt.Errorf("database init: %w", err)
	}

	conn, err := db.GetConnection()
	if err != nil {
		return fmt.Errorf("database connect: %w", err)
	}
	defer conn.Close()

	repo := db.NewRepo(conn)

	ts := handlers.NewTokenSaverConfig(cCtx.Bool("rtk"), cCtx.Bool("caveman"), cCtx.Bool("ponytail"))
	if settings, sErr := repo.GetSettings(); sErr == nil && settings != nil {
		rtk := settings.RTKEnabled
		if cCtx.IsSet("rtk") {
			rtk = cCtx.Bool("rtk")
		}
		caveman := settings.CavemanEnabled
		if cCtx.IsSet("caveman") {
			caveman = cCtx.Bool("caveman")
		}
		ponytail := settings.PonytailEnabled
		if cCtx.IsSet("ponytail") {
			ponytail = cCtx.Bool("ponytail")
		}
		ts.SetAll(rtk, caveman, ponytail)
		ts.SetCaveman(caveman, settings.CavemanLevel)
		ts.SetPonytail(ponytail, settings.PonytailLevel)
	}
	ts.SetInjectionGuard(!cCtx.Bool("no-injection-guard"))
	log.Printf("[config] token savers — rtk=%v caveman=%v (%s) ponytail=%v (%s)", ts.RTKEnabled(), ts.CavemanEnabled(), ts.CavemanLevel(), ts.PonytailEnabled(), ts.PonytailLevel())
	autoUpdate := cCtx.Bool("auto-update")
	if !autoUpdate && repo != nil {
		if settings, sErr := repo.GetSettings(); sErr == nil && settings != nil {
			autoUpdate = settings.AutoUpdate
		}
	}
	updater.StartBackgroundCheck(context.Background(), autoUpdate)
	log.Printf("[config] auto-update enabled=%v", autoUpdate)
	catalogPath := filepath.Join(filepath.Dir(cfg.DatabasePath), "model-catalog.json")
	providers.StartBackgroundCatalogSync(context.Background(), nil, catalogPath)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.MaxBody(middleware.DefaultMaxBodySize))
	r.Use(chiMiddleware.Recoverer)

	r.Use(middleware.RequestLogger)

	handlers.SetupServerRouter(r, repo, ts)

	addr := fmt.Sprintf(":%d", cfg.Port)
	log.Printf("9Router Go Proxy (%s) starting on port %d", updater.CurrentVersion, cfg.Port)

	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)

	server := &http.Server{
		Addr:    addr,
		Handler: r,
	}

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	fmt.Fprintf(os.Stdout, "\n  🚀 9Router Go Proxy (%s) on %s\n\n", updater.CurrentVersion, addr)
	log.Printf("Server is ready to handle requests at %s", addr)

	<-signals // first signal → begin graceful shutdown
	fmt.Fprintln(os.Stdout, "\n  Shutting down...")

	// Signal in-flight SSE streams to end promptly: the stall reader closes each
	// upstream body, handlers emit a final [DONE], and Shutdown completes well
	// within its deadline instead of waiting out the full timeout.
	shutdown.Cancel()

	// A second signal force-quits immediately (e.g. a stream stuck mid-drain).
	go func() {
		<-signals
		fmt.Fprintln(os.Stdout, "\n  Force quitting...")
		os.Exit(1)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		// Log instead of Fatalf so the deferred conn.Close()/logFile.Close()
		// still run; SQLite WAL recovers any straggler on next open.
		log.Printf("Server shutdown did not complete in time: %v", err)
	} else {
		log.Println("Server stopped gracefully")
	}
	return nil
}
