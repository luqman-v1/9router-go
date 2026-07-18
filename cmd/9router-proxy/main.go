package main

import (
	"fmt"
	"log"
	"net/http"

	"9router/proxy/internal/constants"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/urfave/cli/v2"

	"9router/proxy/internal/config"
	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers"
	"9router/proxy/internal/middleware"
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
		Name:  "9router-proxy",
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
		Action: runServer,
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatal(err)
	}
}

func runServer(cCtx *cli.Context) error {
	// Logging: LOG_FILE env or stderr
	if logPath := os.Getenv("LOG_FILE"); logPath != "" {
		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err == nil {
			log.SetOutput(logFile)
			defer logFile.Close()
		} else {
			log.Printf("[config] warning: cannot open LOG_FILE=%s, using stderr: %v", logPath, err)
		}
	}

	// Load configuration from environment variables and platform defaults
	cfg := config.LoadConfig()

	// Initialize global database connection
	if err := db.InitGlobalDatabase(cfg.DatabasePath); err != nil {
		return fmt.Errorf("database init: %w", err)
	}

	conn, err := db.GetConnection()
	if err != nil {
		return fmt.Errorf("database connect: %w", err)
	}
	defer conn.Close()

	repo := db.NewRepo(conn)

	// Token saver — CLI flags override env defaults
	ts := handlers.NewTokenSaverConfig(cCtx.Bool("rtk"), cCtx.Bool("caveman"), cCtx.Bool("ponytail"))
	log.Printf("[config] token savers — rtk=%v caveman=%v ponytail=%v", ts.RTKEnabled(), ts.CavemanEnabled(), ts.PonytailEnabled())

	// Create chi router with standard middleware
	r := chi.NewRouter()
	r.Use(chiMiddleware.Recoverer)
	r.Use(chiMiddleware.RequestID)

	// Strip /v1 prefix so routes register as /messages, /chat/completions, etc.
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

	// Health check endpoint (no auth required)
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
		w.Write([]byte(`{"status":"ok"}`))
	})

	// API key-protected routes
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireApiKey(repo))
		handlers.SetupRoutes(r, repo, ts)
		r.Get("/models", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
			w.Write([]byte(`{"data":[]}`))
		})
	})

	addr := fmt.Sprintf(":%d", cfg.Port)
	log.Printf("9Router Go Proxy starting on port %d", cfg.Port)

	// Graceful shutdown
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
