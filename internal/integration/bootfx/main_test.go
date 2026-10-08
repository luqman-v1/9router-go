//go:build integration

package bootfx

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/fx"

	"9router/proxy/internal/app"
	"9router/proxy/internal/config"
	"9router/proxy/internal/db"
	"9router/proxy/internal/shutdown"
	"9router/proxy/internal/updater"
)

// The whole package shares ONE application instance, started by TestMain and
// stopped when the run ends. That is not a convenience: app.DatabaseModule
// goes through db.InitGlobalDatabase, a process-wide sync.Once, and its fx
// OnStop hook closes the connection for good. A second boot in the same binary
// would therefore reuse a closed handle, and app.ServerModule's OnStop also
// cancels the process-global shutdown context the background workers share.
// Booting per test would make every boot after the first a lie.

const testAPIKey = "sk-bootfx-integration-key"

var (
	// baseURL is the booted server's address; repo is backed by the very
	// database the server opened.
	baseURL string
	repo    *db.Repo

	// upstream is the fake provider every test shares. It records the model of
	// the last request so a test can assert what actually left the gateway.
	upstream *httptest.Server

	upstreamMu    sync.Mutex
	upstreamModel string

	// teardownFailures collects assertions TestMain makes after m.Run, where no
	// *testing.T exists to report them on.
	teardownFailures []string
)

// chatCompletionJSON is the canned OpenAI reply from the fake provider.
const chatCompletionJSON = `{
  "id": "chatcmpl-bootfx",
  "object": "chat.completion",
  "created": 1700000000,
  "model": "upstream-model",
  "choices": [{"index": 0, "message": {"role": "assistant", "content": "booted reply"}, "finish_reason": "stop"}],
  "usage": {"prompt_tokens": 9, "completion_tokens": 4, "total_tokens": 13}
}`

// TestMain boots the production fx graph — ConfigModule, DatabaseModule,
// HandlersModule and ServerModule — once, against a temporary data directory
// and a free port, then serves the tests against it. The environment is set
// with os.Setenv because t.Setenv does not exist outside a test.
func TestMain(m *testing.M) {
	dataDir, err := os.MkdirTemp("", "bootfx-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create temp data dir:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dataDir)

	// These keep config.LoadConfigFromViper and auth.CLIToken's file
	// derivation off the developer's real ~/.9router.
	env := map[string]string{
		"DATA_DIR":         dataDir,
		"JWT_SECRET":       "bootfx-jwt-secret",
		"INITIAL_PASSWORD": "bootfx-password",
		// The updater must never apply a release, and its five-second background
		// check must never leave the process: the manifest URL points at a local
		// server reporting the current version, so both the primary fetch and the
		// GitHub fallback are unreachable.
		"AUTO_UPDATE": "false",
	}
	for key, value := range env {
		if serr := os.Setenv(key, value); serr != nil {
			fmt.Fprintln(os.Stderr, "set env", key+":", serr)
			os.Exit(1)
		}
	}
	manifest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"version":%q,"latestVersion":%q}`, updater.CurrentVersion, updater.CurrentVersion)
	}))
	defer manifest.Close()
	if serr := os.Setenv("UPDATE_URL", manifest.URL); serr != nil {
		fmt.Fprintln(os.Stderr, "set UPDATE_URL:", serr)
		os.Exit(1)
	}

	upstream = newFakeUpstream()

	// The config is supplied directly rather than through viper, so the test
	// never depends on a .env file in the package directory. PORT=0 is not an
	// option: config forces 20130 for any value <= 0.
	port, perr := freePort()
	if perr != nil {
		fmt.Fprintln(os.Stderr, "reserve a port:", perr)
		os.Exit(1)
	}
	cfg := &config.Config{
		Host:            "127.0.0.1",
		Port:            port,
		DatabasePath:    filepath.Join(dataDir, "db", "data.sqlite"),
		JWTSecret:       "bootfx-jwt-secret",
		InitialPassword: "bootfx-password",
		APIKeySecret:    "bootfx-api-key-secret",
		MachineIDSalt:   "bootfx-machine-salt",
	}

	shutdown.TestReset()
	fxApp := fx.New(app.AppModule, fx.Replace(cfg), fx.NopLogger)
	startCtx, cancelStart := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelStart()
	if serr := fxApp.Start(startCtx); serr != nil {
		fmt.Fprintln(os.Stderr, "start application:", serr)
		os.Exit(1)
	}

	baseURL = fmt.Sprintf("http://127.0.0.1:%d", port)
	// ServerModule binds the port from a goroutine, so fxApp.Start returning
	// does not mean the listener is accepting yet.
	if herr := waitForHealth(10 * time.Second); herr != nil {
		fmt.Fprintln(os.Stderr, "server never became healthy:", herr)
		os.Exit(1)
	}

	conn, cerr := db.GetConnection()
	if cerr != nil {
		fmt.Fprintln(os.Stderr, "get database connection:", cerr)
		os.Exit(1)
	}
	repo = db.NewRepo(conn)
	// A closed singleton would be swallowed by ProvideDatabase's best-effort
	// schema handling, so prove the handle is live before any test relies on it.
	if perr := conn.PingContext(context.Background()); perr != nil {
		fmt.Fprintln(os.Stderr, "database handle is not usable:", perr)
		os.Exit(1)
	}

	code := m.Run()

	// The shutdown path the binary uses on SIGTERM: the fx OnStop hook drains
	// and closes the listener. Probe the port afterwards so a broken shutdown
	// is reported here rather than as a mystery in the next run.
	stopCtx, cancelStop := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelStop()
	if serr := fxApp.Stop(stopCtx); serr != nil {
		teardownFailures = append(teardownFailures, fmt.Sprintf("stop application: %v", serr))
	} else if !portReleased(port, 5*time.Second) {
		teardownFailures = append(teardownFailures, fmt.Sprintf("port %d was not released after shutdown", port))
	}

	for _, failure := range teardownFailures {
		fmt.Fprintln(os.Stderr, "FAIL:", failure)
	}
	if len(teardownFailures) > 0 {
		code = 1
	}
	os.Exit(code)
}

// newFakeUpstream starts the shared provider fake. It records the model of the
// last request under a mutex: the handler runs on the server's goroutine and
// the test reads it from its own.
func newFakeUpstream() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read upstream body: "+err.Error(), http.StatusInternalServerError)
			return
		}
		var payload struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &payload)

		upstreamMu.Lock()
		upstreamModel = payload.Model
		upstreamMu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, chatCompletionJSON)
	}))
}

// lastUpstreamModel returns the model the gateway sent most recently.
func lastUpstreamModel() string {
	upstreamMu.Lock()
	defer upstreamMu.Unlock()
	return upstreamModel
}

// freePort reserves and releases a port so the server can bind it. The window
// between Close and ListenAndServe is small and unavoidable: ProvideServer
// creates its own listener rather than accepting one.
func freePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("reserve a port: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return 0, fmt.Errorf("release reserved port %d: %w", port, err)
	}
	return port, nil
}

// waitForHealth polls GET /health until the listener answers.
func waitForHealth(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(baseURL + "/health")
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("GET /health = %d (%s)", resp.StatusCode, strings.TrimSpace(string(body)))
		} else {
			lastErr = err
		}
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("no healthy response within %s: %w", timeout, lastErr)
}

// portReleased reports whether the port can be bound again within timeout. The
// grace period covers the in-flight keep-alive connections the shutdown hook
// refuses first.
func portReleased(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			_ = listener.Close()
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}
