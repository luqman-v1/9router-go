package app_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"9router/proxy/internal/app"
	"9router/proxy/internal/config"
	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers"
	"github.com/go-chi/chi/v5"
	"github.com/spf13/viper"
	"go.uber.org/fx"
	_ "modernc.org/sqlite"
)

func TestAppModule_Validate(t *testing.T) {
	err := fx.ValidateApp(
		app.AppModule,
		fx.NopLogger,
	)
	if err != nil {
		t.Fatalf("AppModule dependency graph failed validation: %v", err)
	}
}

func TestCLIParams(t *testing.T) {
	defaults := app.DefaultCLIParams()
	if !defaults.RTK {
		t.Errorf("expected RTK to be enabled by default")
	}
	if defaults.ADHD {
		t.Errorf("expected ADHD to be disabled by default")
	}

	fromNil := app.NewCLIParams(nil)
	if fromNil.RTK != defaults.RTK {
		t.Errorf("expected NewCLIParams(nil) to equal DefaultCLIParams")
	}
	if fromNil.ADHD != defaults.ADHD {
		t.Errorf("expected fromNil.ADHD to match default")
	}

	t.Setenv("ADHD_ENABLED", "true")
	withEnv := app.DefaultCLIParams()
	if !withEnv.ADHD || !withEnv.ADHDSet {
		t.Errorf("expected ADHD enabled via env")
	}
}

func TestConfigModule(t *testing.T) {
	var cfg *config.Config
	var cfgVal config.Config
	var params app.CLIParams
	var paramsPtr *app.CLIParams
	var v *viper.Viper

	fxApp := fx.New(
		app.ConfigModule,
		fx.NopLogger,
		fx.Populate(&cfg, &cfgVal, &params, &paramsPtr, &v),
	)

	if err := fxApp.Err(); err != nil {
		t.Fatalf("ConfigModule failed to initialize: %v", err)
	}
	if v == nil {
		t.Fatal("expected *viper.Viper to be provided")
	}
	if cfg == nil {
		t.Fatal("expected *config.Config to be provided")
	}
	if cfgVal.Port != cfg.Port {
		t.Errorf("expected config value Port %d, got %d", cfg.Port, cfgVal.Port)
	}
	if paramsPtr == nil || paramsPtr.RTK != params.RTK {
		t.Errorf("expected params pointer to match params value")
	}
}

func TestDatabaseModule(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.sqlite")

	testCfg := &config.Config{
		DatabasePath: dbPath,
		Port:         20131,
	}

	var conn *sql.DB
	var repo *db.Repo

	fxApp := fx.New(
		fx.Supply(testCfg),
		app.DatabaseModule,
		fx.NopLogger,
		fx.Populate(&conn, &repo),
	)

	ctx := context.Background()
	if err := fxApp.Start(ctx); err != nil {
		t.Fatalf("DatabaseModule Start failed: %v", err)
	}
	if conn == nil {
		t.Fatal("expected *sql.DB to be provided")
	}
	if repo == nil {
		t.Fatal("expected *db.Repo to be provided")
	}

	// Verify database is operational
	if err := conn.Ping(); err != nil {
		t.Errorf("expected database to be pingable: %v", err)
	}

	// Stop Fx and verify database hook closes connection
	if err := fxApp.Stop(ctx); err != nil {
		t.Errorf("DatabaseModule Stop failed: %v", err)
	}
	if err := conn.Ping(); err == nil {
		t.Errorf("expected database ping to fail after OnStop close")
	}
}

func TestHandlersModule(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.sqlite")

	testCfg := &config.Config{
		DatabasePath: dbPath,
		Port:         20132,
	}

	var ts *handlers.TokenSaverConfig
	var router *chi.Mux
	var handler http.Handler
	fxApp := fx.New(
		app.ConfigModule,
		fx.Replace(testCfg),
		app.DatabaseModule,
		app.HandlersModule,
		fx.NopLogger,
		fx.Populate(&ts, &router, &handler),
	)

	ctx := context.Background()
	if err := fxApp.Start(ctx); err != nil {
		t.Fatalf("HandlersModule Start failed: %v", err)
	}
	defer fxApp.Stop(ctx)

	if ts == nil {
		t.Fatal("expected *handlers.TokenSaverConfig to be provided")
	}
	if router == nil {
		t.Fatal("expected *chi.Mux to be provided")
	}
	if handler == nil {
		t.Fatal("expected http.Handler to be provided")
	}

	// Verify /health route on router
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200 from /health, got %d", rec.Code)
	}
}

func TestNewApp_FullLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.sqlite")

	// Use an ephemeral port or test config
	os.Setenv("DATABASE_PATH", dbPath)
	os.Setenv("PORT", "20139")
	defer os.Unsetenv("DATABASE_PATH")
	defer os.Unsetenv("PORT")

	params := app.CLIParams{
		RTK:        true,
		AutoUpdate: false,
	}

	var server *http.Server
	fxApp := app.NewApp(params, fx.Populate(&server))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := fxApp.Start(ctx); err != nil {
		t.Fatalf("full app Start failed: %v", err)
	}
	if server == nil {
		t.Fatal("expected *http.Server to be provided")
	}

	if err := fxApp.Stop(ctx); err != nil {
		t.Errorf("full app Stop failed: %v", err)
	}
}
