package clientconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 3.1's stopping condition: every path matches matrix §3, and no string still
// holds "~" or an unexpanded variable.
func TestPathResolution(t *testing.T) {
	// opencode and crush honour $XDG_CONFIG_HOME, which a developer's shell and
	// some CI runners set. Clearing it here is what makes the expectation
	// `home/.config/...` true everywhere; without it the test read the runner's
	// real config path and failed on Linux while passing on Windows.
	t.Setenv("XDG_CONFIG_HOME", "")
	home := t.TempDir()

	tests := []struct {
		name   string
		paths  Paths
		want   []string
		wantIn []string // substrings that must appear, for XDG-dependent shapes
	}{
		{
			name:  "claude",
			paths: claudePaths(home),
			want:  []string{filepath.Join(home, ".claude", "settings.json")},
		},
		{
			name:  "codex",
			paths: codexPaths(home),
			want:  []string{filepath.Join(home, ".codex", "config.toml")},
		},
		{
			name:  "opencode",
			paths: opencodePaths(home),
			want:  []string{filepath.Join(home, ".config", "opencode", "opencode.json")},
		},
		{
			name:  "droid",
			paths: droidPaths(home),
			want:  []string{filepath.Join(home, ".factory", "settings.json")},
		},
		{
			name:  "crush",
			paths: crushPaths(home),
			want:  []string{filepath.Join(home, ".config", "crush", "crush.json")},
		},
		{
			name:  "kilo",
			paths: kiloPaths(home),
			want:  []string{filepath.Join(home, ".local", "share", "kilo", "auth.json")},
		},
		{
			name:  "cline state and secrets",
			paths: clinePaths(home),
			want: []string{
				filepath.Join(home, ".cline", "data", "globalState.json"),
				filepath.Join(home, ".cline", "data", "secrets.json"),
			},
		},
		{
			name:  "pi prefers agent/models.json",
			paths: piPaths(home),
			want:  []string{filepath.Join(home, ".pi", "agent", "models.json")},
		},
		{
			name:  "omp",
			paths: ompPaths(home),
			want:  []string{filepath.Join(home, ".omp", "agent", "models.yml")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if len(tt.paths.Files) != len(tt.want) {
				t.Fatalf("got %d files, want %d", len(tt.paths.Files), len(tt.want))
			}
			for i, f := range tt.paths.Files {
				if f.Path != tt.want[i] {
					t.Errorf("file %d: got %q, want %q", i, f.Path, tt.want[i])
				}
				// Resolved absolutely, with nothing left unexpanded.
				if !filepath.IsAbs(f.Path) {
					t.Errorf("file %d: %q is not absolute", i, f.Path)
				}
				for _, bad := range []string{"~", "$HOME", "%USERPROFILE%", "${"} {
					if strings.Contains(f.Path, bad) {
						t.Errorf("file %d: %q still holds %q", i, f.Path, bad)
					}
				}
				// The guard base must be an ancestor of the file, and must not be
				// the home directory itself — §2 says the base is the nearest
				// directory the adapter may touch.
				if f.Dir == home {
					t.Errorf("file %d: guard base is the home directory", i)
				}
				rel, err := filepath.Rel(f.Dir, f.Path)
				if err != nil || strings.HasPrefix(rel, "..") {
					t.Errorf("file %d: %q is not under its base %q", i, f.Path, f.Dir)
				}
			}
		})
	}
}

// TestPathResolutionXDG pins the one environment variable the matrix §2 allows
// a path to depend on.
func TestPathResolutionXDG(t *testing.T) {
	home := t.TempDir()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)

	got := crushPaths(home).Files[0].Path
	want := filepath.Join(xdg, "crush", "crush.json")
	if got != want {
		t.Errorf("with XDG_CONFIG_HOME set: got %q, want %q", got, want)
	}

	t.Setenv("XDG_CONFIG_HOME", "")
	got = crushPaths(home).Files[0].Path
	want = filepath.Join(home, ".config", "crush", "crush.json")
	if got != want {
		t.Errorf("with XDG_CONFIG_HOME unset: got %q, want %q", got, want)
	}
}

// TestPiPathsLegacyFallback covers matrix §3.8: the older ~/.pi/models.json is
// used only when the agent-scoped one does not exist.
func TestPiPathsLegacyFallback(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, ".pi", "models.json")
	writeFixture(t, legacy, "{}")

	got := piPaths(home).Files[0].Path
	if got != legacy {
		t.Fatalf("got %q, want the legacy file %q", got, legacy)
	}

	// Creating the preferred file flips the choice back.
	preferred := filepath.Join(home, ".pi", "agent", "models.json")
	writeFixture(t, preferred, "{}")
	got = piPaths(home).Files[0].Path
	if got != preferred {
		t.Errorf("got %q, want the agent-scoped file %q", got, preferred)
	}
}

// TestWithV1 pins the rule that eight clients end in /v1 and one does not.
func TestWithV1(t *testing.T) {
	tests := []struct{ in, want string }{
		{"http://localhost:20130", "http://localhost:20130/v1"},
		{"http://localhost:20130/", "http://localhost:20130/v1"},
		{"http://localhost:20130/v1", "http://localhost:20130/v1"},
		{"http://localhost:20130/v1/", "http://localhost:20130/v1"},
		{"https://host/gw", "https://host/gw/v1"},
		{"  http://h  ", "http://h/v1"},
	}
	for _, tt := range tests {
		if got := withV1(tt.in); got != tt.want {
			t.Errorf("withV1(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestOriginOnly pins cline's one difference: it takes the bare origin.
func TestOriginOnly(t *testing.T) {
	tests := []struct{ in, want string }{
		{"http://localhost:20130/v1", "http://localhost:20130"},
		{"http://localhost:20130/v1/", "http://localhost:20130"},
		{"http://localhost:20130", "http://localhost:20130"},
		{"https://host/gw/v1", "https://host/gw"},
		// A path that merely starts with v1 is not a version suffix.
		{"http://h/v1beta", "http://h/v1beta"},
	}
	for _, tt := range tests {
		if got := originOnly(tt.in); got != tt.want {
			t.Errorf("originOnly(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// writeFixture creates a file and any missing parents in a test's temp tree.
func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
