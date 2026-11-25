package main

import (
	"bufio"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/urfave/cli/v2"

	"9router/proxy/internal/daemon"
)

func TestParseChoice(t *testing.T) {
	cases := map[string]string{
		"":        "1", // Enter takes the default
		"\n":      "1",
		"1\n":     "1",
		"3":       "3",
		"6\n":     "6",
		" 5 \n":   "5",
		"0\n":     "0",
		"q\n":     "0",
		"bogus\n": "0",
	}
	for in, want := range cases {
		if got := parseChoice(in); got != want {
			t.Errorf("parseChoice(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestReadChoiceEOFIsNotABlankLine: the caller tells "no operator at all" from
// "the operator pressed Ctrl+Z" by whether anything arrived, so an empty read
// has to be distinguishable from a blank line.
func TestReadChoiceEOFIsNotABlankLine(t *testing.T) {
	if line, ok := readChoice(bufio.NewReader(strings.NewReader(""))); ok {
		t.Errorf("readChoice on an empty stream = (%q, true), want ok=false", line)
	}
	if line, ok := readChoice(bufio.NewReader(strings.NewReader("\n"))); !ok || line != "\n" {
		t.Errorf("readChoice(blank line) = (%q, %v), want (\\n, true)", line, ok)
	}
	// A line with no trailing newline still carries the operator's input.
	if line, ok := readChoice(bufio.NewReader(strings.NewReader("3"))); !ok || line != "3" {
		t.Errorf("readChoice(\"3\" at EOF) = (%q, %v), want (3, true)", line, ok)
	}
}

func TestTrayBinaryName(t *testing.T) {
	name := trayBinaryName()
	if !strings.HasSuffix(name, "-tray") {
		t.Errorf("expected tray binary name to end with -tray, got %q", name)
	}
}

func setupStartTrayStubs(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	self := filepath.Join(dir, binaryName())
	tray := filepath.Join(dir, trayBinaryName())
	if runtime.GOOS == "windows" {
		self += ".exe"
		tray += ".exe"
	}
	if err := os.WriteFile(self, []byte("self"), 0o755); err != nil {
		t.Fatalf("write dummy self: %v", err)
	}
	if err := os.WriteFile(tray, []byte("tray"), 0o755); err != nil {
		t.Fatalf("write dummy tray: %v", err)
	}

	origSelf := menuSelfExecutable
	origRunning := menuDaemonRunning
	origStart := menuDaemonStart
	origSpawn := menuSpawnTray
	t.Cleanup(func() {
		menuSelfExecutable = origSelf
		menuDaemonRunning = origRunning
		menuDaemonStart = origStart
		menuSpawnTray = origSpawn
	})

	menuSelfExecutable = func() (string, error) { return self, nil }
	return tray
}

func testCLIContext() *cli.Context {
	return cli.NewContext(&cli.App{}, flag.NewFlagSet("test", flag.ContinueOnError), nil)
}

func TestStartTray_StartsDaemonWhenStopped(t *testing.T) {
	wantTray := setupStartTrayStubs(t)

	var order []string
	var gotExe string
	menuDaemonRunning = func() int { return 0 }
	menuDaemonStart = func(_ string, _ []string) (daemon.StartResult, error) {
		order = append(order, "daemon")
		return daemon.StartResult{PID: 42}, nil
	}
	menuSpawnTray = func(exe string, _ int) error {
		order = append(order, "tray")
		gotExe = exe
		return nil
	}

	if err := startTray(testCLIContext()); err != nil {
		t.Fatalf("startTray() unexpected error: %v", err)
	}
	if len(order) != 2 || order[0] != "daemon" || order[1] != "tray" {
		t.Fatalf("call order = %v, want [daemon tray]", order)
	}
	if gotExe != wantTray {
		t.Fatalf("spawned tray = %q, want %q", gotExe, wantTray)
	}
}

func TestStartTray_SkipsDaemonStartWhenAlreadyRunning(t *testing.T) {
	setupStartTrayStubs(t)

	daemonCalled := false
	trayCalled := false
	menuDaemonRunning = func() int { return 1234 }
	menuDaemonStart = func(_ string, _ []string) (daemon.StartResult, error) {
		daemonCalled = true
		return daemon.StartResult{}, nil
	}
	menuSpawnTray = func(_ string, _ int) error {
		trayCalled = true
		return nil
	}

	if err := startTray(testCLIContext()); err != nil {
		t.Fatalf("startTray() unexpected error: %v", err)
	}
	if daemonCalled {
		t.Fatal("menuDaemonStart should not be called when daemon is already running")
	}
	if !trayCalled {
		t.Fatal("menuSpawnTray should be called")
	}
}

func TestStartTray_ToleratesErrAlreadyRunningAndPropagatesRealError(t *testing.T) {
	t.Run("tolerates ErrAlreadyRunning", func(t *testing.T) {
		setupStartTrayStubs(t)
		trayCalled := false
		menuDaemonRunning = func() int { return 0 }
		menuDaemonStart = func(_ string, _ []string) (daemon.StartResult, error) {
			return daemon.StartResult{}, daemon.ErrAlreadyRunning
		}
		menuSpawnTray = func(_ string, _ int) error {
			trayCalled = true
			return nil
		}

		if err := startTray(testCLIContext()); err != nil {
			t.Fatalf("startTray() unexpected error: %v", err)
		}
		if !trayCalled {
			t.Fatal("expected tray to spawn on ErrAlreadyRunning")
		}
	})

	t.Run("propagates real error", func(t *testing.T) {
		setupStartTrayStubs(t)
		trayCalled := false
		wantErr := errors.New("port busy")
		menuDaemonRunning = func() int { return 0 }
		menuDaemonStart = func(_ string, _ []string) (daemon.StartResult, error) {
			return daemon.StartResult{}, wantErr
		}
		menuSpawnTray = func(_ string, _ int) error {
			trayCalled = true
			return nil
		}

		if err := startTray(testCLIContext()); !errors.Is(err, wantErr) {
			t.Fatalf("startTray() error = %v, want %v", err, wantErr)
		}
		if trayCalled {
			t.Fatal("tray should not spawn when daemon start fails")
		}
	})
}
