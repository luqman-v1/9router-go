package service

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestWindowsTaskGuardsOffWindows(t *testing.T) {
	stubHost(t, hostStub{goos: "linux", euid: 1000})
	stubRun(t)

	if _, err := installWindowsTask("C:\\9router-go.exe", nil); err == nil {
		t.Error("installWindowsTask() error = nil off windows, want the guard error")
	}
	if _, ok := uninstallWindowsTask(); ok {
		t.Error("uninstallWindowsTask() ok = true off windows, want false")
	}
	if _, ok := statusWindowsTask(); ok {
		t.Error("statusWindowsTask() ok = true off windows, want false")
	}
}

func TestInstallWindowsTaskOnWindows(t *testing.T) {
	stubHost(t, hostStub{goos: "windows"})
	stubWindowsTaskUser(t, "bima")
	calls := stubRun(t)

	info, err := installWindowsTask(`C:\9router-go.exe`, []string{"--rtk=true"})
	if err != nil {
		t.Fatalf("installWindowsTask() error = %v", err)
	}
	if info.Backend != BackendWindowsTask || !info.Enabled || !info.Active {
		t.Errorf("Info = %+v, want windows-task enabled and active", info)
	}
	if info.Path != windowsTaskName {
		t.Errorf("Info.Path = %q, want %q", info.Path, windowsTaskName)
	}
	var create *svcCall
	for i := range *calls {
		if (*calls)[i].name == "schtasks.exe" && slices.Contains((*calls)[i].args, "/create") {
			create = &(*calls)[i]
		}
	}
	if create == nil {
		t.Fatalf("calls = %v, want a schtasks /create", *calls)
	}
	// The task must not run as SYSTEM (different %APPDATA%, needs elevation).
	if slices.Contains(create.args, "SYSTEM") {
		t.Errorf("schtasks /create args = %v, must not run as SYSTEM", create.args)
	}
	if !slices.Contains(create.args, "onlogon") {
		t.Errorf("schtasks /create args = %v, want /sc onlogon", create.args)
	}
	if !slices.Contains(create.args, "/it") {
		t.Errorf("schtasks /create args = %v, want /it (interactive-only, no stored password)", create.args)
	}
	if slices.Contains(create.args, "/rp") {
		t.Errorf("schtasks /create args = %v, must not store a password with /rp", create.args)
	}
	if !slices.Contains(create.args, "/ru") || !slices.Contains(create.args, "bima") {
		t.Errorf("schtasks /create args = %v, want /ru for the installing user", create.args)
	}
	if slices.Contains(create.args, "/rl") {
		t.Errorf("schtasks /create args = %v, must not request /rl highest", create.args)
	}
	if got := argAfter(create.args, "/tr"); got != `C:\9router-go.exe --rtk=true serve` {
		t.Errorf("/tr value = %q, want global flags before serve", got)
	}

	*calls = nil
	if _, err := installWindowsTask(`C:\9router-go.exe`, nil); err != nil {
		t.Fatalf("installWindowsTask(nil args) error = %v", err)
	}
	for _, c := range *calls {
		if c.name == "schtasks.exe" && slices.Contains(c.args, "/create") {
			if got := argAfter(c.args, "/tr"); got != `C:\9router-go.exe serve` {
				t.Errorf("no-args /tr value = %q, want %q", got, `C:\9router-go.exe serve`)
			}
		}
	}
}

// TestWindowsCommandLineQuotesForCmd: the /tr value is read by cmd.exe, so a
// spaced path is wrapped verbatim, not escaped the way strconv.Quote would.
func TestWindowsCommandLineQuotesForCmd(t *testing.T) {
	got := windowsCommandLine(`C:\Program Files\9router\9router-go.exe`, []string{"--rtk=true"})
	want := `"C:\Program Files\9router\9router-go.exe" --rtk=true`
	if got != want {
		t.Errorf("windowsCommandLine() = %q, want %q", got, want)
	}
	if strings.Contains(got, `\\`) {
		t.Errorf("windowsCommandLine() = %q, must not escape backslashes for cmd.exe", got)
	}
}

func argAfter(args []string, flag string) string {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func TestInstallWindowsTaskErrorPaths(t *testing.T) {
	t.Run("create failure is reported and not enabled", func(t *testing.T) {
		stubHost(t, hostStub{goos: "windows"})
		stubRunFunc(t, func(name string, args ...string) (string, error) {
			if slices.Contains(args, "/create") {
				return "access denied", errors.New("boom")
			}
			return "", nil
		})

		info, err := installWindowsTask(`C:\9router-go.exe`, nil)
		if err == nil {
			t.Fatal("installWindowsTask() error = nil when /create fails")
		}
		if info.Enabled {
			t.Errorf("Info.Enabled = true after a failed create: %+v", info)
		}
	})

	t.Run("run failure leaves the task enabled but inactive", func(t *testing.T) {
		stubHost(t, hostStub{goos: "windows"})
		stubRunFunc(t, func(name string, args ...string) (string, error) {
			if slices.Contains(args, "/run") {
				return "", errors.New("boom")
			}
			return "", nil
		})

		info, err := installWindowsTask(`C:\9router-go.exe`, nil)
		if err != nil {
			t.Fatalf("installWindowsTask() error = %v", err)
		}
		if !info.Enabled || info.Active {
			t.Errorf("Info = %+v, want enabled but inactive", info)
		}
	})
}

func TestStatusWindowsTask(t *testing.T) {
	tests := []struct {
		name     string
		state    string
		probeErr error
		wantOK   bool
		wantAct  bool
		wantUnk  bool
	}{
		{name: "task present and running", state: "4", wantOK: true, wantAct: true},
		// Ready is ordinal 3; matching the localized "Running" string broke
		// non-English Windows.
		{name: "task present but ready", state: "3", wantOK: true, wantAct: false},
		{name: "task disabled", state: "1", wantOK: true, wantAct: false},
		{name: "query error", probeErr: errors.New("no task"), wantOK: false},
		// PowerShell absent or blocked: report the state unknown, not false.
		{name: "unprobeable state is reported as unknown", state: "", wantOK: true, wantUnk: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubHost(t, hostStub{goos: "windows"})
			stubRunFunc(t, func(name string, args ...string) (string, error) {
				if tt.probeErr != nil {
					return "", tt.probeErr
				}
				if strings.Contains(name, "powershell") {
					return tt.state, nil
				}
				return windowsTaskName + "  Ready", nil
			})

			info, ok := statusWindowsTask()
			if ok != tt.wantOK {
				t.Fatalf("statusWindowsTask() ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if info.Active != tt.wantAct || info.ActiveUnknown != tt.wantUnk {
				t.Errorf("Active = %v, ActiveUnknown = %v, want %v, %v", info.Active, info.ActiveUnknown, tt.wantAct, tt.wantUnk)
			}
		})
	}
}

func TestUninstallWindowsTask(t *testing.T) {
	t.Run("task present is ended and deleted", func(t *testing.T) {
		stubHost(t, hostStub{goos: "windows"})
		calls := stubRunFunc(t, func(name string, args ...string) (string, error) {
			if slices.Contains(args, "/query") {
				return windowsTaskName + "  Ready", nil
			}
			return "", nil
		})

		if _, ok := uninstallWindowsTask(); !ok {
			t.Fatal("uninstallWindowsTask() = false, want true")
		}
		var ended, deleted bool
		for _, c := range *calls {
			if slices.Contains(c.args, "/end") {
				ended = true
			}
			if slices.Contains(c.args, "/delete") {
				deleted = true
			}
		}
		if !ended || !deleted {
			t.Errorf("calls = %v, want /end then /delete", *calls)
		}
	})

	t.Run("query error means nothing to remove", func(t *testing.T) {
		stubHost(t, hostStub{goos: "windows"})
		stubRunFunc(t, func(string, ...string) (string, error) { return "", errors.New("not found") })
		if _, ok := uninstallWindowsTask(); ok {
			t.Error("uninstallWindowsTask() = true when the task does not exist")
		}
	})

	t.Run("output without the task name means nothing to remove", func(t *testing.T) {
		stubHost(t, hostStub{goos: "windows"})
		stubRunFunc(t, func(string, ...string) (string, error) { return "some other task", nil })
		if _, ok := uninstallWindowsTask(); ok {
			t.Error("uninstallWindowsTask() = true without the task name in the query output")
		}
	})
}

func TestControlWindowsTask(t *testing.T) {
	tests := []struct {
		op    string
		flags []string
	}{
		{"start", []string{"/run"}},
		{"stop", []string{"/end"}},
		{"restart", []string{"/end", "/run"}},
	}
	for _, tt := range tests {
		t.Run(tt.op, func(t *testing.T) {
			calls := stubRun(t)
			if err := controlWindowsTask(tt.op); err != nil {
				t.Fatalf("controlWindowsTask(%s) error = %v", tt.op, err)
			}
			for _, flag := range tt.flags {
				found := false
				for _, c := range *calls {
					if slices.Contains(c.args, flag) {
						found = true
					}
				}
				if !found {
					t.Errorf("calls = %v, want a %s invocation", *calls, flag)
				}
			}
		})
	}

	t.Run("unknown operation is rejected", func(t *testing.T) {
		stubRun(t)
		if err := controlWindowsTask("explode"); err == nil {
			t.Error("controlWindowsTask(explode) error = nil, want error")
		}
	})
}
