package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"9router/proxy/internal/config"
	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers"
)

func main() {
	// Load configuration from environment variables and platform defaults
	cfg := config.LoadConfig()

	// Initialize global database connection
	if err := db.InitGlobalDatabase(cfg.DatabasePath); err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	conn, err := db.GetConnection()
	if err != nil {
		log.Fatalf("Failed to get database connection: %v", err)
	}
	defer conn.Close()

	repo := db.NewRepo(conn)

	// Create chi router with standard middleware
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)

	// Health check endpoint (no auth required)
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	// API key-protected routes
	r.Group(func(r chi.Router) {
		r.Use(handlers.RequireApiKey(repo))

		// Chat routes (OpenAI /v1/chat/completions and Claude /v1/messages)
		handlers.SetupRoutes(r, repo)

		// Models listing stub
		r.Get("/v1/models", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
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

	log.Printf("Server is ready to handle requests at %s", addr)
	<-done
	log.Println("Server stopped gracefully")
}
