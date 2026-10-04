package app_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"9router/proxy/internal/app"
	"9router/proxy/internal/config"
	"9router/proxy/internal/db"
	"github.com/go-chi/chi/v5"
	"go.uber.org/fx"
)

// newTestServer boots ServerModule the way production does — through fx, so
// the assertions cover the struct ProvideServer actually builds, not a
// hand-rolled copy of it — then hands back the *http.Server without letting
// the fx hooks run. Starting them would bind the port and start the updater
// and catalog-sync loops, none of which this test needs.
func newTestServer(t *testing.T, handler http.Handler) *http.Server {
	t.Helper()
	if handler == nil {
		r := chi.NewRouter()
		r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		handler = r
	}

	cfg := &config.Config{
		DatabasePath: t.TempDir() + "/test.sqlite",
		Port:         0, // never bound: hooks stay unstarted
	}

	// Only the lifecycle hooks touch the repo, and they never run here, so an
	// opened temp database is enough to satisfy the dependency graph.
	var repo *db.Repo
	dbApp := fx.New(
		app.ConfigModule,
		fx.Replace(cfg),
		app.DatabaseModule,
		fx.NopLogger,
		fx.Populate(&repo),
	)
	if err := dbApp.Err(); err != nil {
		t.Fatalf("database graph failed: %v", err)
	}
	if err := dbApp.Start(context.Background()); err != nil {
		t.Fatalf("database Start failed: %v", err)
	}
	t.Cleanup(func() { _ = dbApp.Stop(context.Background()) })

	var server *http.Server
	srvApp := fx.New(
		fx.Provide(func() *config.Config { return cfg }),
		fx.Provide(func() *db.Repo { return repo }),
		fx.Provide(func() app.CLIParams { return app.CLIParams{} }),
		fx.Provide(app.ProvideServer),
		fx.Provide(func() http.Handler { return handler }),
		fx.Populate(&server),
	)
	if err := srvApp.Err(); err != nil {
		t.Fatalf("server graph failed: %v", err)
	}

	return server
}

// TestServer_ConnectionLimitsAreEnforced pins the bounds on how long one client
// can hold one connection. ReadHeaderTimeout and IdleTimeout were both zero on
// the server this repo shipped until the Slowloris fix, and zero is the value
// that fails: a client could open a socket, dribble headers a byte at a time,
// and keep the descriptor until the process died.
//
// MaxHeaderBytes is pinned for a different reason and is not part of that
// hole. net/http already refuses an unbounded header block — Server zero falls
// back to http.DefaultMaxHeaderBytes (1 MiB), so a zero here caps headers at
// the same 1 MiB the explicit value does. Setting it states the limit this
// gateway intends rather than inheriting whatever the stdlib default happens
// to be, and the test fails if that statement is dropped.
//
// These are asserted on the built server rather than re-checked in a
// behavioural test because the production values are deliberately too large
// to exercise in a test suite (10s and 120s), and because the failure the timeouts
// guard against is exactly the field silently reverting to its zero value —
// a re-checked deadline would still pass if someone dropped the field. What is
// asserted here is also the contract the SSE and WebSocket paths depend on
// alongside it: WriteTimeout must stay zero, since internal/proxy/stall.go
// lets a stream idle for DefaultStallTimeout.
func TestServer_ConnectionLimitsAreEnforced(t *testing.T) {
	srv := newTestServer(t, nil)

	if srv.ReadHeaderTimeout == 0 {
		t.Error("ReadHeaderTimeout is 0: a client can hold a connection open by dribbling headers")
	} else if srv.ReadHeaderTimeout > 30*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want at most 30s for a slow-header guard", srv.ReadHeaderTimeout)
	}

	if srv.IdleTimeout == 0 {
		t.Error("IdleTimeout is 0: abandoned keep-alive connections are never reaped")
	} else if srv.IdleTimeout < time.Minute {
		t.Errorf("IdleTimeout = %v, want at least 1m so dashboard keep-alive survives a normal pause", srv.IdleTimeout)
	}

	if srv.MaxHeaderBytes == 0 {
		t.Error("MaxHeaderBytes is 0: the gateway no longer states its own header limit" +
			" (net/http would still apply its 1 MiB default)")
	} else if srv.MaxHeaderBytes > 4<<20 {
		t.Errorf("MaxHeaderBytes = %v, want at most 4 MiB", srv.MaxHeaderBytes)
	}

	// The gateway streams: SSE bodies from internal/proxy/sse_*,
	// usage_stream, consolelog, and the Gemini Live WebSocket. A write
	// deadline would sever all of them mid-response, so it must stay unset.
	if srv.WriteTimeout != 0 {
		t.Errorf("WriteTimeout = %v, want 0: it would cut off long-lived SSE and WebSocket streams", srv.WriteTimeout)
	}
}
