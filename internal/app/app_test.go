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
	"9router/proxy/internal/shutdown"
	"github.com/go-chi/chi/v5"
	"github.com/spf13/viper"
	"github.com/urfave/cli/v2"
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

func TestNewCLIParams_WithContext(t *testing.T) {
	appCLI := cli.NewApp()
	appCLI.Flags = []cli.Flag{
		&cli.BoolFlag{Name: "rtk"},
		&cli.BoolFlag{Name: "caveman"},
		&cli.BoolFlag{Name: "ponytail"},
		&cli.BoolFlag{Name: "adhd"},
		&cli.BoolFlag{Name: "auto-update"},
		&cli.BoolFlag{Name: "no-injection-guard"},
	}
	appCLI.Action = func(cCtx *cli.Context) error {
		p := app.NewCLIParams(cCtx)
		if !p.RTK || !p.RTKSet {
			t.Error("expected RTK true and RTKSet true")
		}
		if !p.Caveman || !p.CavemanSet {
			t.Error("expected Caveman true and CavemanSet true")
		}
		if !p.Ponytail || !p.PonytailSet {
			t.Error("expected Ponytail true and PonytailSet true")
		}
		if !p.ADHD || !p.ADHDSet {
			t.Error("expected ADHD true and ADHDSet true")
		}
		if !p.AutoUpdate {
			t.Error("expected AutoUpdate true")
		}
		if !p.NoInjectionGuard {
			t.Error("expected NoInjectionGuard true")
		}
		return nil
	}
	_ = appCLI.Run([]string{"9router", "--rtk", "--caveman", "--ponytail", "--adhd", "--auto-update", "--no-injection-guard"})
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

// resetProcessGlobals drops the two package-level states a DatabaseModule boot
// leaves behind. Without this the whole file is order-dependent under
// `go test -shuffle`: whichever test boots first owns the one global
// connection, every later boot reuses it, and the fx Stop that closes it makes
// all of them fail — a shuffle failure that has nothing to do with the code
// under test.
func resetProcessGlobals(t *testing.T) {
	t.Helper()
	db.ResetGlobalDatabaseForTest(t)
	shutdown.TestReset()
}

func TestDatabaseModule(t *testing.T) {
	resetProcessGlobals(t)
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

// TestDatabaseModule_SecondBootAfterAFirstOneWasClosed is the regression test
// for the order-dependent failure. Before, `go test -shuffle` on this package
// failed on TestDatabaseModule whenever another DatabaseModule boot ran first:
// the process-wide sync.Once handed out one handle, the first test's fx Stop
// closed it, and this test then pinged a connection that could never open.
// Two consecutive boots must both get a working database.
func TestDatabaseModule_SecondBootAfterAFirstOneWasClosed(t *testing.T) {
	resetProcessGlobals(t)

	boot := func(t *testing.T) *sql.DB {
		t.Helper()
		cfg := &config.Config{
			DatabasePath: filepath.Join(t.TempDir(), "test.sqlite"),
			Port:         20133,
		}
		var conn *sql.DB
		fxApp := fx.New(
			fx.Supply(cfg),
			app.DatabaseModule,
			fx.NopLogger,
			fx.Populate(&conn),
		)
		if err := fxApp.Start(context.Background()); err != nil {
			t.Fatalf("DatabaseModule Start failed: %v", err)
		}
		t.Cleanup(func() { _ = fxApp.Stop(context.Background()) })
		return conn
	}

	first := boot(t)
	if err := first.Ping(); err != nil {
		t.Fatalf("first boot is not usable: %v", err)
	}

	// The first boot's cleanup closed the handle. A second boot must not
	// inherit that closed one.
	resetProcessGlobals(t)

	second := boot(t)
	if err := second.Ping(); err != nil {
		t.Fatalf("second boot inherited a closed handle: %v", err)
	}
	if second == first {
		t.Error("second boot reused the first handle after it was closed")
	}
}

func TestHandlersModule(t *testing.T) {
	resetProcessGlobals(t)
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
	resetProcessGlobals(t)
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

func TestProvideConfigValue_Nil(t *testing.T) {
	val := app.ProvideConfigValue(nil)
	if val.Port != 0 {
		t.Errorf("expected empty config value, got port %d", val.Port)
	}
}

func TestDefaultFxLogger_Env(t *testing.T) {
	t.Setenv("FX_LOGGING", "true")
	opt := app.DefaultFxLogger()
	if opt == nil {
		t.Error("expected non-nil Fx option when FX_LOGGING=true")
	}

	t.Setenv("FX_LOGGING", "false")
	optNop := app.DefaultFxLogger()
	if optNop == nil {
		t.Error("expected non-nil Fx option when FX_LOGGING=false")
	}
}

func TestProvideTokenSaverConfig_EnvOverrides(t *testing.T) {
	t.Setenv("RTK_ENABLED", "false")
	t.Setenv("CAVEMAN_ENABLED", "true")
	t.Setenv("PONYTAIL_ENABLED", "true")
	t.Setenv("ADHD_ENABLED", "true")

	params := app.CLIParams{
		RTK:         false,
		RTKSet:      true,
		Caveman:     true,
		CavemanSet:  true,
		Ponytail:    true,
		PonytailSet: true,
		ADHD:        true,
		ADHDSet:     true,
	}
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}
	defer database.Close()
	repo := db.NewRepo(database)
	tsc := app.ProvideTokenSaverConfig(repo, params)
	if tsc.RTKEnabled() != false {
		t.Errorf("expected RTK false, got %v", tsc.RTKEnabled())
	}
	if tsc.CavemanEnabled() != true {
		t.Errorf("expected Caveman true, got %v", tsc.CavemanEnabled())
	}
	if tsc.PonytailEnabled() != true {
		t.Errorf("expected Ponytail true, got %v", tsc.PonytailEnabled())
	}
	if tsc.ADHDEnabled() != true {
		t.Errorf("expected ADHD true, got %v", tsc.ADHDEnabled())
	}
}

func TestRun_StartFailure(t *testing.T) {
	failingApp := fx.New(
		fx.NopLogger,
		fx.Invoke(func(lc fx.Lifecycle) {
			lc.Append(fx.Hook{
				OnStart: func(context.Context) error {
					return os.ErrInvalid
				},
			})
		}),
	)
	err := app.Run(failingApp)
	if err == nil {
		t.Fatal("expected Run to return startup error")
	}
}

func TestProvideServer_WithHost(t *testing.T) {
	var lc fx.Lifecycle = &fxLifecycleStub{}
	params := app.ServerParams{
		Config: &config.Config{
			Host: "127.0.0.1",
			Port: 20145,
		},
		Lifecycle: lc,
		Handler:   http.NotFoundHandler(),
	}
	srv := app.ProvideServer(params)
	if srv.Addr != "127.0.0.1:20145" {
		t.Errorf("expected 127.0.0.1:20145, got %s", srv.Addr)
	}
}

type fxLifecycleStub struct{}

func (s *fxLifecycleStub) Append(fx.Hook) {}
