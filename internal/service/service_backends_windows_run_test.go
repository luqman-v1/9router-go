package service

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// TestHelperWindowsRunExitError spawns a fast non-zero child process so tests
// on both Windows and Linux can construct a genuine *exec.ExitError without
// relying on platform-specific shells (/bin/false vs cmd.exe).
func TestHelperWindowsRunExitError(t *testing.T) {
	if os.Getenv("GO_WANT_EXIT_ERROR_HELPER") != "1" {
		return
	}
	_, _ = fmt.Fprint(os.Stderr, os.Getenv("GO_HELPER_STDERR"))
	os.Exit(1)
}

func fakeExitError(t *testing.T, rawOutput string) error {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperWindowsRunExitError$")
	cmd.Env = append(os.Environ(),
		"GO_WANT_EXIT_ERROR_HELPER=1",
		"GO_HELPER_STDERR="+rawOutput,
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("helper subprocess unexpectedly exited 0")
	}
	return fmt.Errorf("service: schtasks.exe /create /tn 9router-go /f: %w: %s", err, strings.TrimSpace(string(out)))
}

func TestWindowsRunGuardsOffWindows(t *testing.T) {
	stubHost(t, hostStub{goos: "linux", euid: 1000})
	stubRun(t)

	if _, err := installWindowsRun("/usr/local/bin/9router-go", nil); err == nil {
		t.Error("installWindowsRun() error = nil off windows, want guard error")
	}
	if _, ok := uninstallWindowsRun(); ok {
		t.Error("uninstallWindowsRun() ok = true off windows, want false")
	}
	if _, ok := statusWindowsRun(); ok {
		t.Error("statusWindowsRun() ok = true off windows, want false")
	}
}

func TestInstallWindowsRun(t *testing.T) {
	t.Run("writes quoted start command and returns enabled unsupervised info", func(t *testing.T) {
		stubHost(t, hostStub{goos: "windows", nonElevated: true})
		entries := stubWindowsRun(t, nil)

		info, err := installWindowsRun(`C:\Program Files\9router\9router-go.exe`, []string{"--rtk=true"})
		if err != nil {
			t.Fatalf("installWindowsRun() error = %v", err)
		}
		if info.Backend != BackendWindowsRun {
			t.Errorf("Backend = %q, want %q", info.Backend, BackendWindowsRun)
		}
		if !info.Enabled || info.Active {
			t.Errorf("Enabled=%v Active=%v, want true/false", info.Enabled, info.Active)
		}
		wantPath := windowsRunDisplayPath()
		if info.Path != wantPath {
			t.Errorf("Path = %q, want %q", info.Path, wantPath)
		}
		wantCmd := `"C:\Program Files\9router\9router-go.exe" --rtk=true start`
		if info.Exec != wantCmd || entries[windowsTaskName] != wantCmd {
			t.Errorf("Exec=%q entry=%q, want %q", info.Exec, entries[windowsTaskName], wantCmd)
		}
		if strings.Contains(entries[windowsTaskName], `\\`) {
			t.Errorf("entry = %q, must not escape backslashes", entries[windowsTaskName])
		}
	})

	t.Run("propagates registry write failure cleanly", func(t *testing.T) {
		stubHost(t, hostStub{goos: "windows", nonElevated: true})
		prev := windowsRunWrite
		windowsRunWrite = func(string, string) error { return errors.New("registry write blocked") }
		t.Cleanup(func() { windowsRunWrite = prev })

		info, err := installWindowsRun(`C:\9router-go.exe`, nil)
		if err == nil {
			t.Fatal("installWindowsRun() error = nil on write failure")
		}
		if info.Enabled {
			t.Errorf("Info.Enabled = true on failed write: %+v", info)
		}
	})
}

func TestStatusAndUninstallWindowsRun(t *testing.T) {
	t.Run("status reads present, absent, and error states", func(t *testing.T) {
		stubHost(t, hostStub{goos: "windows"})
		stubWindowsRun(t, nil)
		if _, ok := statusWindowsRun(); ok {
			t.Fatal("statusWindowsRun() = true when Run key has no entry")
		}

		stubWindowsRun(t, map[string]string{windowsTaskName: `"C:\9router-go.exe" start`})
		info, ok := statusWindowsRun()
		if !ok || info.Backend != BackendWindowsRun || !info.Enabled {
			t.Fatalf("statusWindowsRun() = (%+v, %v), want windows-run enabled", info, ok)
		}

		prev := windowsRunRead
		windowsRunRead = func(string) (string, bool, error) { return "", false, errors.New("boom") }
		t.Cleanup(func() { windowsRunRead = prev })
		if _, ok := statusWindowsRun(); ok {
			t.Fatal("statusWindowsRun() = true on read error, want false")
		}
	})

	t.Run("uninstall removes present entry and handles absent/error states", func(t *testing.T) {
		stubHost(t, hostStub{goos: "windows"})
		entries := stubWindowsRun(t, map[string]string{windowsTaskName: `"C:\9router-go.exe" start`})
		info, ok := uninstallWindowsRun()
		if !ok || info.Backend != BackendWindowsRun {
			t.Fatalf("uninstallWindowsRun() = (%+v, %v), want true", info, ok)
		}
		if _, exists := entries[windowsTaskName]; exists {
			t.Fatal("entry remained after uninstallWindowsRun")
		}
		if _, ok := uninstallWindowsRun(); ok {
			t.Fatal("second uninstallWindowsRun() = true, want false")
		}

		prev := windowsRunDelete
		windowsRunDelete = func(string) (bool, error) { return false, errors.New("boom") }
		t.Cleanup(func() { windowsRunDelete = prev })
		if _, ok := uninstallWindowsRun(); ok {
			t.Fatal("uninstallWindowsRun() = true on delete error, want false")
		}
	})
}

func TestInstallWindowsFallback(t *testing.T) {
	t.Run("unelevated session skips schtasks and installs windows-run", func(t *testing.T) {
		setHome(t)
		redirectInitPaths(t)
		stubHost(t, hostStub{
			goos:        "windows",
			nonElevated: true,
			bins:        map[string]string{"schtasks.exe": `C:\Windows\System32\schtasks.exe`},
		})
		calls := stubRun(t)
		entries := stubWindowsRun(t, nil)

		info, err := Install([]string{"--rtk=true"})
		if err != nil {
			t.Fatalf("Install() error = %v", err)
		}
		if info.Backend != BackendWindowsRun || !info.Enabled {
			t.Fatalf("Info = %+v, want windows-run enabled", info)
		}
		for _, c := range *calls {
			if c.name == "schtasks.exe" {
				t.Errorf("unexpected schtasks call on unelevated session: %+v", c)
			}
		}
		if entries[windowsTaskName] == "" {
			t.Error("expected HKCU Run value to be written")
		}
	})

	t.Run("falls back to windows-run when schtasks fails with non-English ExitError", func(t *testing.T) {
		setHome(t)
		redirectInitPaths(t)
		stubHost(t, hostStub{
			goos: "windows",
			bins: map[string]string{"schtasks.exe": `C:\Windows\System32\schtasks.exe`},
		})
		entries := stubWindowsRun(t, nil)
		stubRunFunc(t, func(name string, args ...string) (string, error) {
			if slices.Contains(args, "/create") {
				return "FEHLER: Zugriff verweigert.", fakeExitError(t, "FEHLER: Zugriff verweigert.")
			}
			return "", nil
		})

		info, err := Install(nil)
		if err != nil {
			t.Fatalf("Install() error = %v", err)
		}
		if info.Backend != BackendWindowsRun || entries[windowsTaskName] == "" {
			t.Fatalf("got (%+v, entries=%v), want fallback to windows-run", info, entries)
		}
	})

	t.Run("preserves schtasks raw diagnostic when fallback write also fails", func(t *testing.T) {
		setHome(t)
		redirectInitPaths(t)
		stubHost(t, hostStub{
			goos: "windows",
			bins: map[string]string{"schtasks.exe": `C:\Windows\System32\schtasks.exe`},
		})
		prevWrite := windowsRunWrite
		windowsRunWrite = func(string, string) error { return errors.New("hkcu denied") }
		t.Cleanup(func() { windowsRunWrite = prevWrite })
		stubRunFunc(t, func(name string, args ...string) (string, error) {
			if slices.Contains(args, "/create") {
				return "ERROR: Access is denied.", fakeExitError(t, "ERROR: Access is denied.")
			}
			return "", nil
		})

		_, err := Install(nil)
		if err == nil {
			t.Fatal("Install() error = nil when both schtasks and fallback fail")
		}
		if !strings.Contains(err.Error(), "ERROR: Access is denied.") || !strings.Contains(err.Error(), "hkcu denied") {
			t.Errorf("error %q should include both raw schtasks output and fallback failure", err.Error())
		}
	})

	t.Run("forced windows-task override does not fall back silently", func(t *testing.T) {
		setHome(t)
		redirectInitPaths(t)
		t.Setenv(BackendOverrideEnv, string(BackendWindowsTask))
		stubHost(t, hostStub{
			goos:        "windows",
			nonElevated: true,
			bins:        map[string]string{"schtasks.exe": `C:\Windows\System32\schtasks.exe`},
		})
		entries := stubWindowsRun(t, nil)
		stubRunFunc(t, func(name string, args ...string) (string, error) {
			if slices.Contains(args, "/create") {
				return "ERROR: Access is denied.", fakeExitError(t, "ERROR: Access is denied.")
			}
			return "", nil
		})

		if _, err := Install(nil); err == nil {
			t.Fatal("Install() error = nil with forced windows-task failure")
		}
		if len(entries) != 0 {
			t.Errorf("forced windows-task must not write Run entry, got %v", entries)
		}
	})

	t.Run("forced windows-run override installs windows-run even when elevated", func(t *testing.T) {
		setHome(t)
		redirectInitPaths(t)
		t.Setenv(BackendOverrideEnv, string(BackendWindowsRun))
		stubHost(t, hostStub{
			goos: "windows",
			bins: map[string]string{"schtasks.exe": `C:\Windows\System32\schtasks.exe`},
		})
		calls := stubRun(t)
		entries := stubWindowsRun(t, nil)

		info, err := Install(nil)
		if err != nil {
			t.Fatalf("Install() error = %v", err)
		}
		if info.Backend != BackendWindowsRun || entries[windowsTaskName] == "" {
			t.Fatalf("got (%+v, entries=%v), want windows-run", info, entries)
		}
		if len(*calls) != 0 {
			t.Errorf("unexpected run calls: %+v", *calls)
		}
	})
}

func TestDetectStatusUninstallAndControlWindowsRun(t *testing.T) {
	t.Run("Detect honors windows-run and windows-task overrides", func(t *testing.T) {
		stubHost(t, hostStub{goos: "linux"})
		t.Setenv(BackendOverrideEnv, "windows-run")
		if got := Detect(); got != BackendWindowsRun {
			t.Errorf("Detect() = %q, want %q", got, BackendWindowsRun)
		}
		t.Setenv(BackendOverrideEnv, "windows-task")
		if got := Detect(); got != BackendWindowsTask {
			t.Errorf("Detect() = %q, want %q", got, BackendWindowsTask)
		}
	})

	t.Run("Status prefers scheduled task when present, else reports windows-run", func(t *testing.T) {
		setHome(t)
		redirectInitPaths(t)
		stubHost(t, hostStub{goos: "windows"})
		stubCrontab(t, "")
		stubWindowsRun(t, map[string]string{windowsTaskName: `"C:\9router-go.exe" start`})
		stubRunFunc(t, func(string, ...string) (string, error) {
			return "", errors.New("task not found")
		})

		info, ok := Status()
		if !ok || info.Backend != BackendWindowsRun {
			t.Fatalf("Status() = (%+v, %v), want windows-run", info, ok)
		}

		stubRunFunc(t, func(name string, args ...string) (string, error) {
			if slices.Contains(args, "/query") {
				return windowsTaskName + "  Ready", nil
			}
			if strings.Contains(name, "powershell") {
				return "4", nil
			}
			return "", nil
		})
		info, ok = Status()
		if !ok || info.Backend != BackendWindowsTask {
			t.Fatalf("Status() with task = (%+v, %v), want windows-task", info, ok)
		}
	})

	t.Run("Uninstall removes windows-run alone and both task + Run when both exist", func(t *testing.T) {
		setHome(t)
		redirectInitPaths(t)
		stubHost(t, hostStub{goos: "windows"})
		stubCrontab(t, "")
		entries := stubWindowsRun(t, map[string]string{windowsTaskName: `"C:\9router-go.exe" start`})
		stubRunFunc(t, func(string, ...string) (string, error) {
			return "", errors.New("no task")
		})

		removed, err := Uninstall()
		if err != nil || len(removed) != 1 || removed[0].Backend != BackendWindowsRun {
			t.Fatalf("Uninstall() = (%+v, %v), want [windows-run]", removed, err)
		}
		if len(entries) != 0 {
			t.Fatalf("entries left behind: %v", entries)
		}

		entries[windowsTaskName] = `"C:\9router-go.exe" start`
		stubRunFunc(t, func(name string, args ...string) (string, error) {
			if slices.Contains(args, "/query") {
				return windowsTaskName + "  Ready", nil
			}
			return "", nil
		})
		removed, err = Uninstall()
		if err != nil || len(removed) != 2 {
			t.Fatalf("Uninstall(both) = (%+v, %v), want both removed", removed, err)
		}
		if removed[0].Backend != BackendWindowsTask || removed[1].Backend != BackendWindowsRun || len(entries) != 0 {
			t.Fatalf("Uninstall(both) = %+v, entries=%v", removed, entries)
		}
	})

	t.Run("Start/Stop/Restart return ErrNoSupervisor for windows-run", func(t *testing.T) {
		setHome(t)
		redirectInitPaths(t)
		stubHost(t, hostStub{goos: "windows"})
		stubCrontab(t, "")
		stubWindowsRun(t, map[string]string{windowsTaskName: `"C:\9router-go.exe" start`})
		stubRunFunc(t, func(string, ...string) (string, error) {
			return "", errors.New("no task")
		})

		for _, fn := range []func() (Info, error){Start, Stop, Restart} {
			if _, err := fn(); !errors.Is(err, ErrNoSupervisor) {
				t.Errorf("control error = %v, want ErrNoSupervisor", err)
			}
		}
	})
}
