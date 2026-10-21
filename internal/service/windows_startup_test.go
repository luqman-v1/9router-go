package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// windowsStartupTestPath is the launcher path under a temp %APPDATA%.
func windowsStartupTestPath(appData string) string {
	return filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "Startup", windowsStartupFile)
}

func TestRenderWindowsStartupVBS(t *testing.T) {
	out := renderWindowsStartupVBS(`C:\Program Files\9router-go.exe`, []string{"--rtk=true"})

	if !strings.Contains(out, windowsStartupMarker) {
		t.Errorf("launcher missing the ownership marker:\n%s", out)
	}
	// The exe path (with a space) and its argument must reach Run as one
	// string literal, with embedded quotes doubled for VBScript.
	if !strings.Contains(out, `"""C:\Program Files\9router-go.exe"" start --rtk=true"`) {
		t.Errorf("launcher command is not a single VBS string literal:\n%s", out)
	}
	// Window style 0 hides the console; async (False) so the launcher exits.
	if !strings.Contains(out, ", 0, False") {
		t.Errorf("launcher must run hidden and not wait:\n%s", out)
	}
}

func TestInstallWindowsStartupWritesLauncherAndStarts(t *testing.T) {
	stubHost(t, hostStub{goos: "windows"})
	appData := t.TempDir()
	t.Setenv("APPDATA", appData)
	calls := stubRun(t)

	info, err := installWindowsStartup(`C:\Tools\9router-go.exe`, []string{"--rtk=true"})
	if err != nil {
		t.Fatalf("installWindowsStartup() error = %v", err)
	}
	path := windowsStartupTestPath(appData)
	if !fileExists(path) {
		t.Fatalf("launcher not written to %s", path)
	}
	if info.Backend != BackendWindowsStartup || !info.Enabled || !info.Active {
		t.Errorf("Info = %+v, want windows-startup enabled and active", info)
	}
	if info.Path != path {
		t.Errorf("Info.Path = %q, want %q", info.Path, path)
	}
	if !hasCall(*calls, "wscript.exe", path) {
		t.Errorf("calls = %v, want a wscript.exe launch of the launcher", *calls)
	}
}

func TestInstallWindowsStartupGuardsOffWindows(t *testing.T) {
	stubHost(t, hostStub{goos: "linux", euid: 1000})
	if _, err := installWindowsStartup("/usr/local/bin/9router-go", nil); err == nil {
		t.Error("installWindowsStartup() error = nil off windows, want the guard error")
	}
}

func TestUninstallWindowsStartupRemovesLauncher(t *testing.T) {
	stubHost(t, hostStub{goos: "windows"})
	appData := t.TempDir()
	t.Setenv("APPDATA", appData)
	path := windowsStartupTestPath(appData)
	writeFile(t, path, "launcher")

	info, ok := uninstallWindowsStartup()
	if !ok {
		t.Fatal("uninstallWindowsStartup() ok = false, want true")
	}
	if info.Backend != BackendWindowsStartup || info.Path != path {
		t.Errorf("Info = %+v, want backend/path of the removed launcher", info)
	}
	if fileExists(path) {
		t.Errorf("launcher still present after uninstall: %s", path)
	}
	if _, ok := uninstallWindowsStartup(); ok {
		t.Error("second uninstallWindowsStartup() ok = true, want false")
	}
}

func TestStatusWindowsStartup(t *testing.T) {
	stubHost(t, hostStub{goos: "windows"})
	appData := t.TempDir()
	t.Setenv("APPDATA", appData)

	if _, ok := statusWindowsStartup(); ok {
		t.Error("statusWindowsStartup() ok = true with no launcher, want false")
	}

	path := windowsStartupTestPath(appData)
	writeFile(t, path, "launcher")
	info, ok := statusWindowsStartup()
	if !ok {
		t.Fatal("statusWindowsStartup() ok = false with a launcher present")
	}
	if info.Backend != BackendWindowsStartup || !info.Enabled || !info.ActiveUnknown {
		t.Errorf("Info = %+v, want windows-startup enabled with unknown active state", info)
	}
}

// TestInstallFallsBackToStartupWhenTaskDenied: creating a logon task needs an
// elevated console, so a denied schtasks must fall through to the unprivileged
// Startup folder instead of failing the install.
func TestInstallFallsBackToStartupWhenTaskDenied(t *testing.T) {
	stubHost(t, hostStub{goos: "windows", bins: map[string]string{"schtasks.exe": `C:\Windows\System32\schtasks.exe`}})
	stubWindowsTaskUser(t, "tester")
	appData := t.TempDir()
	t.Setenv("APPDATA", appData)
	stubRunFunc(t, func(name string, args ...string) (string, error) {
		if name == "schtasks.exe" {
			return "ERROR: Access is denied.", errDenied
		}
		return "", nil
	})

	info, err := Install([]string{"--rtk=true"})
	if err != nil {
		t.Fatalf("Install() error = %v, want a fallback to the Startup folder", err)
	}
	if info.Backend != BackendWindowsStartup {
		t.Errorf("Install() backend = %q, want %q", info.Backend, BackendWindowsStartup)
	}
	if !fileExists(windowsStartupTestPath(appData)) {
		t.Error("Startup launcher not written after the task was denied")
	}
}

// TestDetectWindowsStartupOverride pins the --backend override so a user can
// force the unprivileged backend on a machine where the task would work.
func TestDetectWindowsStartupOverride(t *testing.T) {
	stubHost(t, hostStub{goos: "windows", bins: map[string]string{"schtasks.exe": "schtasks.exe"}})
	t.Setenv(BackendOverrideEnv, string(BackendWindowsStartup))

	if got := Detect(); got != BackendWindowsStartup {
		t.Errorf("Detect() = %q, want %q", got, BackendWindowsStartup)
	}
}

var errDenied = os.ErrPermission
