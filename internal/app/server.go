package app

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"go.uber.org/fx"

	"9router/proxy/internal/config"
	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy/oauth"
	"9router/proxy/internal/shutdown"
	"9router/proxy/internal/updater"
)

// ServerModule provides *http.Server and manages its lifecycle and background tasks.
var ServerModule = fx.Module("server",
	fx.Provide(
		ProvideServer,
	),
	fx.Invoke(
		func(*http.Server) {},
	),
)

// ServerParams defines inputs for constructing the HTTP server and lifecycle hooks.
type ServerParams struct {
	fx.In

	Lifecycle fx.Lifecycle
	Config    *config.Config
	Repo      *db.Repo
	Handler   http.Handler
	CLIParams CLIParams
}

// Server timeouts are fixed rather than configurable on purpose: the values
// below bound how long one client can hold one connection, so making them
// tunable would put a Slowloris knob in reach of anyone who edits .env.
const (
	// serverReadHeaderTimeout caps how long a client may take to finish
	// sending request headers. Same value as the OAuth callback listener in
	// internal/proxy/oauth/codex_proxy.go.
	serverReadHeaderTimeout = 10 * time.Second
	// serverIdleTimeout closes keep-alive connections with no request in
	// flight. It does not apply while a request is being served, so SSE and
	// WebSocket streams run to their natural end.
	serverIdleTimeout = 120 * time.Second
	// serverMaxHeaderBytes states the header limit the gateway intends.
	// It does not close a hole: net/http falls back to
	// http.DefaultMaxHeaderBytes (1 MiB) when this is zero, so the enforced
	// value is the same either way. Pinning it keeps the limit a decision
	// here rather than a stdlib default this repo never reviewed.
	serverMaxHeaderBytes = 1 << 20
)

// ProvideServer creates *http.Server and registers lifecycle hooks.
func ProvideServer(p ServerParams) *http.Server {
	var addr string
	if p.Config.Host != "" {
		addr = net.JoinHostPort(p.Config.Host, strconv.Itoa(p.Config.Port))
	} else {
		addr = fmt.Sprintf(":%d", p.Config.Port)
	}
	server := &http.Server{
		Addr:    addr,
		Handler: p.Handler,
		// WriteTimeout stays unset on purpose: internal/proxy/stall.go lets an
		// SSE stream idle for DefaultStallTimeout (6 minutes), and a write
		// deadline would sever those streams mid-response.
		ReadHeaderTimeout: serverReadHeaderTimeout,
		IdleTimeout:       serverIdleTimeout,
		MaxHeaderBytes:    serverMaxHeaderBytes,
	}

	p.Lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			autoUpdate := p.CLIParams.AutoUpdate
			if !autoUpdate && p.Repo != nil {
				if settings, sErr := p.Repo.GetSettings(); sErr == nil && settings != nil {
					autoUpdate = settings.AutoUpdate
				}
			}
			updater.StartBackgroundCheck(shutdown.Context(), autoUpdate)
			log.Printf("[config] auto-update enabled=%v", autoUpdate)

			catalogPath := filepath.Join(filepath.Dir(p.Config.DatabasePath), "model-catalog.json")
			providers.StartBackgroundCatalogSync(shutdown.Context(), nil, catalogPath)
			oauth.StartBackgroundRefresh(shutdown.Context(), p.Repo)

			log.Printf("9router-go Proxy (%s) starting on port %d", updater.CurrentVersion, p.Config.Port)

			go func() {
				if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					log.Fatalf("Server failed: %v", err)
				}
			}()

			fmt.Fprintf(os.Stdout, "\n  🚀 9router-go Proxy (%s) on %s\n\n", updater.CurrentVersion, addr)
			log.Printf("Server is ready to handle requests at %s", addr)
			return nil
		},
		OnStop: func(ctx context.Context) error {
			fmt.Fprintln(os.Stdout, "\n  Shutting down...")

			// Signal in-flight SSE streams to end promptly: the stall reader closes each
			// upstream body, handlers emit a final [DONE], and Shutdown completes well
			// within its deadline instead of waiting out the full timeout.
			shutdown.Cancel()

			// Refuse NEW keep-alive reuse immediately: idle browser/dashboard
			// connections otherwise hold Shutdown for the full deadline even
			// when zero requests are in flight (the "shutdown takes forever"
			// complaint). In-flight requests still drain gracefully below.
			server.SetKeepAlivesEnabled(false)

			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := server.Shutdown(shutdownCtx); err != nil {
				log.Printf("Server shutdown did not complete in time: %v", err)
			} else {
				log.Println("Server stopped gracefully")
			}
			return nil
		},
	})

	return server
}
