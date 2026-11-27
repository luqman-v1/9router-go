package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urfave/cli/v2"

	"9router/proxy/internal/config"
)

func TestDoctorCheckFormat(t *testing.T) {
	ok := DoctorCheck{Name: "port", OK: true, Detail: "free"}
	if got := ok.Format(); strings.Contains(got, "FAIL") || strings.Contains(got, "fix:") {
		t.Fatalf("passing check rendered a failure: %q", got)
	}

	bad := DoctorCheck{Name: "port", OK: false, Detail: "held", Fix: "kill <pid>"}
	got := bad.Format()
	if !strings.Contains(got, "FAIL") || !strings.Contains(got, "fix: kill <pid>") {
		t.Fatalf("failing check lost its mark or fix: %q", got)
	}
}

func TestPortReleaseHintNamesPlatformTool(t *testing.T) {
	tests := []struct {
		name    string
		windows bool
		want    []string
		deny    string
	}{
		{name: "windows", windows: true, want: []string{"netstat -ano", "taskkill /PID", "20130"}, deny: "lsof"},
		{name: "unix", windows: false, want: []string{"lsof -i :20130", "kill <pid>"}, deny: "taskkill"},
	}

	original := isWindows
	t.Cleanup(func() { isWindows = original })

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isWindows = func() bool { return tt.windows }
			got := portReleaseHint("http://127.0.0.1:20130")
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("hint %q missing %q", got, want)
				}
			}
			if strings.Contains(got, tt.deny) {
				t.Errorf("hint %q should not mention %q", got, tt.deny)
			}
		})
	}
}

func TestCheckDataDirReportsWritability(t *testing.T) {
	t.Run("missing directory fails", func(t *testing.T) {
		t.Setenv("DATA_DIR", filepath.Join(t.TempDir(), "absent"))
		got := checkDataDir(&config.Config{})
		if got.OK {
			t.Fatalf("missing data dir reported OK: %s", got.Detail)
		}
		if got.Fix == "" {
			t.Error("failure carried no fix")
		}
	})

	t.Run("writable directory passes and leaves no probe", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("DATA_DIR", dir)
		if got := checkDataDir(&config.Config{}); !got.OK {
			t.Fatalf("writable data dir failed: %s (%s)", got.Detail, got.Fix)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("write probe left behind: %v", entries)
		}
	})
}

func TestCheckDatabase(t *testing.T) {
	dir := t.TempDir()

	t.Run("missing file fails", func(t *testing.T) {
		got := checkDatabase(&config.Config{DatabasePath: filepath.Join(dir, "data.sqlite")})
		if got.OK {
			t.Fatalf("missing database reported OK: %s", got.Detail)
		}
	})

	t.Run("existing file passes", func(t *testing.T) {
		path := filepath.Join(dir, "present.sqlite")
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		got := checkDatabase(&config.Config{DatabasePath: path})
		if !got.OK {
			t.Fatalf("existing database failed: %s", got.Detail)
		}
	})

	t.Run("postgres dsn is not a file", func(t *testing.T) {
		got := checkDatabase(&config.Config{DatabasePath: "postgres://user:pass@localhost/db"})
		if !got.OK || !strings.Contains(got.Detail, "postgres") {
			t.Fatalf("postgres dsn handled as a file: %s (%v)", got.Detail, got.OK)
		}
	})
}

func TestCheckPortFreeDoesNotClaimForeignListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// A bare listener answers no /health, so it must be reported as held by
	// something that is not the gateway rather than as a healthy gateway.
	url := "http://" + ln.Addr().String()
	got := checkPort(url)
	if got.OK {
		t.Fatalf("foreign listener reported OK: %s", got.Detail)
	}
	if got.Fix == "" {
		t.Error("foreign listener failure carried no fix")
	}
}

func TestCheckPortFreePasses(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	url := "http://" + ln.Addr().String()
	_ = ln.Close()

	if got := checkPort(url); !got.OK {
		t.Fatalf("closed port reported failure: %s (%s)", got.Detail, got.Fix)
	}
}

func TestCheckPortReportsNonHealthAnswerAsForeign(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not the gateway", http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	got := checkPort(srv.URL)
	if got.OK {
		t.Fatalf("server without /health reported as the gateway: %s", got.Detail)
	}
	if !strings.Contains(got.Detail, "held by another process") {
		t.Fatalf("detail lost the foreign-listener wording: %s", got.Detail)
	}
}

func TestCheckPortReportsGatewayAsHealthy(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	got := checkPort(srv.URL)
	if !got.OK {
		t.Fatalf("gateway answered /health but check failed: %s (%s)", got.Detail, got.Fix)
	}
	if !strings.Contains(got.Detail, "served by the gateway") {
		t.Fatalf("detail lost the gateway wording: %s", got.Detail)
	}
}

// wedgedProbes installs probes describing a daemon that holds the port but never
// answers /health, which is the state the review of #1517 required doctor to
// tell apart from a foreign listener.
func wedgedProbes(t *testing.T, pid int) {
	t.Helper()
	originalPID, originalPort, originalHealthy := runningPID, portStatus, healthyAt
	t.Cleanup(func() {
		runningPID, portStatus, healthyAt = originalPID, originalPort, originalHealthy
	})
	runningPID = func() int { return pid }
	portStatus = func(string) (bool, string) { return true, fmt.Sprintf("pid %d owns it", pid) }
	healthyAt = func(string) bool { return false }
}

func TestCheckPortNamesAWedgedGatewayAsOurs(t *testing.T) {
	wedgedProbes(t, 4242)

	got := checkPort("http://127.0.0.1:20130")
	if got.OK {
		t.Fatalf("a wedged gateway reported OK: %s", got.Detail)
	}
	if !strings.Contains(got.Detail, "wedged") {
		t.Fatalf("detail does not name the state: %s", got.Detail)
	}
	if !strings.Contains(got.Fix, "9router-go restart") {
		t.Fatalf("fix does not offer a restart: %s", got.Fix)
	}
	// The whole point: never advise killing our own daemon.
	if strings.Contains(got.Fix, "taskkill") || strings.Contains(got.Fix, "lsof") {
		t.Fatalf("fix tells the operator to kill their own gateway: %s", got.Fix)
	}
}

func TestCheckPortStillNamesAForeignListenerWhenNoClaimExists(t *testing.T) {
	wedgedProbes(t, 0)

	got := checkPort("http://127.0.0.1:20130")
	if got.OK {
		t.Fatalf("a foreign listener reported OK: %s", got.Detail)
	}
	if !strings.Contains(got.Detail, "another process") {
		t.Fatalf("detail does not name the foreign process: %s", got.Detail)
	}
	if !strings.Contains(got.Fix, "taskkill") && !strings.Contains(got.Fix, "lsof") {
		t.Fatalf("fix lost the port-release command: %s", got.Fix)
	}
}

func TestCheckPortPassesAGatewayThatAnswers(t *testing.T) {
	originalPID, originalPort, originalHealthy := runningPID, portStatus, healthyAt
	t.Cleanup(func() {
		runningPID, portStatus, healthyAt = originalPID, originalPort, originalHealthy
	})
	runningPID = func() int { return 4242 }
	portStatus = func(string) (bool, string) { return true, "pid 4242 owns it" }
	healthyAt = func(string) bool { return true }

	got := checkPort("http://127.0.0.1:20130")
	if !got.OK || !strings.Contains(got.Detail, "served by the gateway") {
		t.Fatalf("a healthy gateway failed the port check: %s (%s)", got.Detail, got.Fix)
	}
}

func TestCheckDaemonFailsForAWedgedGateway(t *testing.T) {
	wedgedProbes(t, 4242)

	got := checkDaemon("http://127.0.0.1:20130")
	if got.OK {
		t.Fatalf("a wedged gateway reported as running: %s", got.Detail)
	}
	if !strings.Contains(got.Detail, "wedged") {
		t.Fatalf("detail does not name the state: %s", got.Detail)
	}
}

func TestCheckDaemonHonorsHealthyAtProbeForLivePID(t *testing.T) {
	originalPID, originalHealthy := runningPID, healthyAt
	t.Cleanup(func() {
		runningPID, healthyAt = originalPID, originalHealthy
	})
	runningPID = func() int { return 4242 }
	healthyAt = func(string) bool { return true }

	got := checkDaemon("http://127.0.0.1:20130")
	if !got.OK {
		t.Fatalf("checkDaemon ignored healthyAt probe for live pid: %s (%s)", got.Detail, got.Fix)
	}
	if !strings.Contains(got.Detail, "running") {
		t.Fatalf("expected running detail, got: %s", got.Detail)
	}
}

func TestDoctorCommandExplicitZeroPortFailsConfigCheck(t *testing.T) {
	originalPID, originalPort, originalHealthy := runningPID, portStatus, healthyAt
	t.Cleanup(func() {
		runningPID, portStatus, healthyAt = originalPID, originalPort, originalHealthy
	})
	runningPID = func() int { return 4242 }
	portStatus = func(string) (bool, string) { return true, "pid 4242 owns it" }
	healthyAt = func(string) bool { return true }

	dir := t.TempDir()
	dbDir := filepath.Join(dir, "db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dbDir, "data.sqlite")
	if err := os.WriteFile(dbPath, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATA_DIR", dir)
	t.Setenv("PORT", "20130")

	oldStdout := os.Stdout
	rPipe, wPipe, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = wPipe
	defer func() {
		os.Stdout = oldStdout
		_ = wPipe.Close()
		_ = rPipe.Close()
	}()

	app := &cli.App{
		Name:     "9router-go",
		Commands: []*cli.Command{doctorCommand()},
	}
	runErr := app.Run([]string{"9router-go", "doctor", "--port", "0"})
	_ = wPipe.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, rPipe)
	out := buf.String()

	if runErr == nil {
		t.Fatalf("expected non-nil error for explicit --port 0, output:\n%s", out)
	}
	if !strings.Contains(out, "[FAIL] config") || !strings.Contains(out, "port 0 is out of range") {
		t.Fatalf("expected config check to reject explicit --port 0, output:\n%s", out)
	}
}

func TestCheckDatabaseRejectsDirectoryPath(t *testing.T) {
	dir := t.TempDir()
	got := checkDatabase(&config.Config{DatabasePath: dir})
	if got.OK {
		t.Fatalf("directory path passed database check: %s", got.Detail)
	}
}
