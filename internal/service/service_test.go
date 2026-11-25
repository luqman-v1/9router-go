package service

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type svcCall struct {
	name string
	args []string
}

// stubRun records supervisor calls instead of invoking the real init system.
func stubRun(t *testing.T) *[]svcCall {
	t.Helper()
	previous := run
	calls := &[]svcCall{}
	run = func(name string, args ...string) (string, error) {
		*calls = append(*calls, svcCall{name: name, args: slices.Clone(args)})
		return "", nil
	}
	t.Cleanup(func() { run = previous })
	return calls
}

// TestSystemdScope pins the single choke point that keeps every systemctl call
// on the per-user manager: this package never touches the system bus.
func TestSystemdScope(t *testing.T) {
	if got := systemdScope(); !slices.Equal(got, []string{"--user"}) {
		t.Errorf("systemdScope() = %v, want [--user]", got)
	}
}

func TestRenderSystemdUnit(t *testing.T) {
	t.Run("user unit wants default target", func(t *testing.T) {
		t.Setenv("DATA_DIR", "")
		unit := renderSystemdUnit("/usr/local/bin/9router-go", []string{"--rtk=true"})
		for _, want := range []string{
			"Description=9router-go",
			"ExecStart=/usr/local/bin/9router-go --rtk=true",
			"WantedBy=default.target",
			"Restart=always",
			"WorkingDirectory=/usr/local/bin",
		} {
			if !strings.Contains(unit, want) {
				t.Errorf("unit missing %q:\n%s", want, unit)
			}
		}
		if strings.Contains(unit, "Environment=") {
			t.Errorf("unit must not bake DATA_DIR when unset:\n%s", unit)
		}
	})

	t.Run("shell DATA_DIR is baked in", func(t *testing.T) {
		t.Setenv("DATA_DIR", "/data/custom dir")
		unit := renderSystemdUnit("/usr/local/bin/9router-go", nil)
		if !strings.Contains(unit, `Environment="DATA_DIR=/data/custom dir"`) {
			t.Errorf("unit must preserve exported DATA_DIR:\n%s", unit)
		}
	})

	t.Run("paths with spaces are quoted", func(t *testing.T) {
		t.Setenv("DATA_DIR", "")
		unit := renderSystemdUnit("/opt/my apps/9router-go", nil)
		if !strings.Contains(unit, `ExecStart="/opt/my apps/9router-go"`) {
			t.Errorf("ExecStart must quote spaced paths:\n%s", unit)
		}
	})

	// WorkingDirectory must stay unquoted: systemd reads the value raw, and a
	// spaced path needs no quoting because whitespace is not a separator there.
	t.Run("spaced working directory is written verbatim", func(t *testing.T) {
		t.Setenv("DATA_DIR", "")
		unit := renderSystemdUnit("/opt/my apps/9router-go", nil)
		if !strings.Contains(unit, "WorkingDirectory=/opt/my apps\n") {
			t.Errorf("WorkingDirectory must carry the whole spaced path unquoted:\n%s", unit)
		}
	})

	t.Run("escapeSystemdPath only doubles percent", func(t *testing.T) {
		tests := []struct{ in, want string }{
			{"/usr/local/bin", "/usr/local/bin"},
			{"/opt/my apps", "/opt/my apps"},
			{`/opt/app\`, `/opt/app\`},
			{`/opt/a"b`, `/opt/a"b`},
			{"/opt/100%win", "/opt/100%%win"},
		}
		for _, tt := range tests {
			if got := escapeSystemdPath(tt.in); got != tt.want {
				t.Errorf("escapeSystemdPath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		}
	})

	// WorkingDirectory must be slash-separated: filepath.Dir yields
	// backslashes on Windows, which systemd cannot resolve.
	t.Run("working directory carries no backslash on any runner", func(t *testing.T) {
		t.Setenv("DATA_DIR", "")
		unit := renderSystemdUnit("/usr/local/bin/9router-go", nil)
		for _, line := range strings.Split(unit, "\n") {
			if strings.HasPrefix(line, "WorkingDirectory=") && strings.Contains(line, `\`) {
				t.Errorf("WorkingDirectory must be slash-separated on every runner: %s", line)
			}
		}
	})
}

func TestRenderLaunchdPlist(t *testing.T) {
	plist := renderLaunchdPlist("/Applications/9router-go", []string{"--rtk=true"})
	for _, want := range []string{
		"<string>com.9router.gateway</string>",
		"<string>/Applications/9router-go</string>",
		"<string>--rtk=true</string>",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist missing %q:\n%s", want, plist)
		}
	}
}

func TestRenderOpenRCScript(t *testing.T) {
	script := renderOpenRCScript("/usr/local/bin/9router-go", []string{"--rtk=true"})
	for _, want := range []string{
		"#!/sbin/openrc-run",
		"command=/usr/local/bin/9router-go",
		`command_args="--rtk=true"`,
		"command_background=\"yes\"",
		"need net",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("openrc script missing %q:\n%s", want, script)
		}
	}
}

func TestRenderRunitRun(t *testing.T) {
	script := renderRunitRun("/usr/local/bin/9router-go", []string{"--rtk=true"})
	if !strings.HasPrefix(script, "#!/bin/sh\n") {
		t.Errorf("runit run script must start with shebang:\n%s", script)
	}
	if !strings.Contains(script, "exec /usr/local/bin/9router-go --rtk=true") {
		t.Errorf("runit run script must exec the gateway:\n%s", script)
	}
}

func TestRenderCronLine(t *testing.T) {
	line := renderCronLine("/usr/local/bin/9router-go", []string{"--rtk=true"})
	if !strings.HasPrefix(line, "@reboot /usr/local/bin/9router-go --rtk=true start ") {
		t.Errorf("cron line must place global flags before start: %q", line)
	}
	if !strings.Contains(line, cronMarker) {
		t.Errorf("cron line must carry the ownership marker: %q", line)
	}

	desktop := renderAutostartDesktop("/usr/local/bin/9router-go", nil)
	for _, want := range []string{"[Desktop Entry]", "Exec=/usr/local/bin/9router-go start", "Terminal=false", cronMarker} {
		if !strings.Contains(desktop, want) {
			t.Errorf("desktop entry missing %q:\n%s", want, desktop)
		}
	}
}

func TestMergeCronEntry(t *testing.T) {
	tests := []struct {
		name     string
		existing string
		wantDup  bool
	}{
		{"empty crontab appends", "", false},
		{"other entries preserved", "0 * * * * /usr/bin/backup\n", false},
		{"exact line is idempotent", "@reboot /bin/9router-go start " + cronMarker + "\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line := "@reboot /bin/9router-go start " + cronMarker
			merged, dup := mergeCronEntry(tt.existing, line)
			if dup != tt.wantDup {
				t.Fatalf("mergeCronEntry() dup = %v, want %v", dup, tt.wantDup)
			}
			if !dup && !strings.Contains(merged, line) {
				t.Errorf("merged crontab missing entry:\n%s", merged)
			}
			if strings.Contains(tt.existing, "/usr/bin/backup") && !strings.Contains(merged, "/usr/bin/backup") {
				t.Errorf("merged crontab dropped user entry:\n%s", merged)
			}
		})
	}
}

func TestStripCronEntries(t *testing.T) {
	existing := "0 * * * * /usr/bin/backup\n@reboot /bin/9router-go start " + cronMarker + "\n"
	content, removed := stripCronEntries(existing)
	if !removed {
		t.Fatal("stripCronEntries() removed = false, want true")
	}
	if strings.Contains(content, cronMarker) {
		t.Errorf("stripped crontab still owns a line:\n%s", content)
	}
	if !strings.Contains(content, "/usr/bin/backup") {
		t.Errorf("stripped crontab dropped user entry:\n%s", content)
	}

	if _, removed := stripCronEntries("0 * * * * /usr/bin/backup\n"); removed {
		t.Error("stripCronEntries() removed = true on a foreign crontab, want false")
	}
}

func TestDetectOverride(t *testing.T) {
	t.Setenv(BackendOverrideEnv, "cron")
	if got := Detect(); got != BackendCron {
		t.Errorf("Detect() = %q, want %q", got, BackendCron)
	}
	t.Setenv(BackendOverrideEnv, "systemd-system")
	if got := Detect(); got != BackendSystemdUser {
		t.Errorf("Detect() = %q, want %q (the retired system scope degrades to the user unit)", got, BackendSystemdUser)
	}
}

func TestQuoteArg(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"--rtk=true", "--rtk=true"},
		{"/opt/my apps/9router-go", `"/opt/my apps/9router-go"`},
		{"", "''"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := quoteArg(tt.input); got != tt.want {
				t.Errorf("quoteArg(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestUninstallSystemdUserScopesEveryCall guards the regression where the
// uninstall calls omitted --user and hit the system manager instead.
func TestUninstallSystemdUserScopesEveryCall(t *testing.T) {
	home := setHome(t)
	unit := filepath.Join(home, ".config", "systemd", "user", unitName)
	if err := os.MkdirAll(filepath.Dir(unit), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unit, []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	calls := stubRun(t)
	if _, ok := uninstallSystemd(); !ok {
		t.Fatal("uninstallSystemd() = false, want true")
	}
	if _, err := os.Stat(unit); !os.IsNotExist(err) {
		t.Fatalf("unit file still present after uninstall: %v", err)
	}

	wantOps := []string{"disable", "daemon-reload", "stop", "reset-failed"}
	if len(*calls) != len(wantOps) {
		t.Fatalf("systemctl calls = %v, want exactly %v", *calls, wantOps)
	}
	for i, op := range wantOps {
		c := (*calls)[i]
		if c.name != "systemctl" {
			t.Errorf("call %d = %s, want systemctl", i, c.name)
		}
		if !slices.Contains(c.args, "--user") {
			t.Errorf("call %d %v missing --user — would hit the system manager", i, c.args)
		}
		if !slices.Contains(c.args, op) {
			t.Errorf("call %d %v missing op %q", i, c.args, op)
		}
	}
}

// TestUninstallLaunchdUnloadsBeforeRemove: stop the supervisor before deleting
// its entry.
func TestUninstallLaunchdUnloadsBeforeRemove(t *testing.T) {
	home := setHome(t)
	plist := filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plist, []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}

	calls := stubRun(t)
	if _, ok := uninstallLaunchd(); !ok {
		t.Fatal("uninstallLaunchd() = false, want true")
	}
	if _, err := os.Stat(plist); !os.IsNotExist(err) {
		t.Fatalf("plist still present after uninstall: %v", err)
	}
	if len(*calls) == 0 {
		t.Fatal("no launchctl call recorded, want unload before removal")
	}
	first := (*calls)[0]
	if first.name != "launchctl" || !slices.Contains(first.args, "unload") {
		t.Fatalf("first call = %v, want launchctl unload first", first)
	}
}

// TestUninstallLaunchdReportsFailureWhenRemovalFails: a failed delete must not
// be reported as success.
func TestUninstallLaunchdReportsFailureWhenRemovalFails(t *testing.T) {
	home := setHome(t)
	plist := filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
	// A non-empty directory at the plist path: Stat succeeds but Remove fails.
	if err := os.MkdirAll(filepath.Join(plist, "keep"), 0o755); err != nil {
		t.Fatal(err)
	}

	stubRun(t)
	if _, ok := uninstallLaunchd(); ok {
		t.Fatal("uninstallLaunchd() = true when removal failed, want false")
	}
}
