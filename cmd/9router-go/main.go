package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/urfave/cli/v2"

	"9router/proxy/internal/config"
	"9router/proxy/internal/constants"
	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers"
	"9router/proxy/internal/middleware"
	"9router/proxy/internal/mitm"
)

// statusWriter wraps http.ResponseWriter to capture the status code.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

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
		},
		Commands: []*cli.Command{
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

func mitmEnable(cCtx *cli.Context) error {
	dataDir := resolveDataDir()
	mgr := mitm.NewManager(dataDir)
	if err := mgr.Enable(); err != nil {
		return fmt.Errorf("MITM enable failed: %w", err)
	}
	fmt.Println("MITM proxy enabled. Intercepted traffic on :443 → 9router proxy.")
	return nil
}

func mitmDisable(_ *cli.Context) error {
	homeDir, _ := os.UserHomeDir()
	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = homeDir + "/.9router"
	}
	mgr := mitm.NewManager(dataDir)
	if err := mgr.Disable(); err != nil {
		return fmt.Errorf("MITM disable failed: %w", err)
	}
	fmt.Println("MITM proxy disabled.")
	return nil
}

func mitmStatus(_ *cli.Context) error {
	homeDir, _ := os.UserHomeDir()
	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = homeDir + "/.9router"
	}
	mgr := mitm.NewManager(dataDir)
	status := mgr.Status()
	fmt.Printf("Running: %v\n", status["running"])
	fmt.Printf("CA installed: %v\n", status["ca_installed"])
	fmt.Printf("MITM dir: %v\n", status["mitm_dir"])
	return nil
}

func resolveDataDir() string {
	d := os.Getenv("DATA_DIR")
	if d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".9router"
	}
	return home + "/.9router"
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
	log.Printf("[config] token savers — rtk=%v caveman=%v ponytail=%v", ts.RTKEnabled(), ts.CavemanEnabled(), ts.PonytailEnabled())

	r := chi.NewRouter()
	r.Use(chiMiddleware.Recoverer)
	r.Use(chiMiddleware.RequestID)

	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			start := time.Now()
			path := req.URL.Path
			for len(path) > 3 && path[:4] == "/v1/" {
				path = path[3:]
			}
			req.URL.Path = path
			ww := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(ww, req)
			log.Printf("[request] %s %s %d %s", req.Method, path, ww.status, time.Since(start))
		})
	})

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
		w.Write([]byte(`{"status":"ok"}`))
	})

	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireApiKey(repo))
		handlers.SetupRoutes(r, repo, ts)
	})

	addr := fmt.Sprintf(":%d", cfg.Port)
	log.Printf("9Router Go Proxy starting on port %d", cfg.Port)

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)

	go func() {
		if err := http.ListenAndServe(addr, r); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	fmt.Fprintf(os.Stdout, "\n  🚀 9Router Go Proxy on %s\n\n", addr)
	log.Printf("Server is ready to handle requests at %s", addr)
	<-done
	fmt.Fprintln(os.Stdout, "\n  Shutting down...")
	log.Println("Server stopped gracefully")
	return nil
}
