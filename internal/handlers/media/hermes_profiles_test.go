package media

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hermesTestHome points HERMES_HOME at a scratch directory for the whole test.
// Every handler in this file resolves its paths through hermesRoot(), so the
// real ~/.hermes is never read or written.
func hermesTestHome(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HERMES_HOME", root)
	return root
}

// writeProfile lays down a profile directory with a config.yaml.
func writeProfile(t *testing.T, root, name, config string) string {
	t.Helper()
	dir := filepath.Join(root, "profiles", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(config), 0o644); err != nil {
		t.Fatalf("WriteFile config.yaml: %v", err)
	}
	return dir
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", path, err)
	}
	return string(b)
}

func TestResolveHermesHome_InvalidNameIsRejected(t *testing.T) {
	hermesTestHome(t)

	tests := []struct {
		name    string
		profile string
	}{
		{"path traversal", "../secrets"},
		{"absolute path", filepath.Join(string(filepath.Separator), "etc")},
		{"nested separator", "a/b"},
		{"leading dash", "-work"},
		{"dot segment", "."},
		{"too long", strings.Repeat("a", 65)},
		{"space", "my profile"},
		{"default is reserved", "Default"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := resolveHermesHome(tt.profile); err == nil {
				t.Fatalf("resolveHermesHome(%q) should have failed", tt.profile)
			}
		})
	}
}

func TestResolveHermesHome_NamedProfileStaysUnderProfilesRoot(t *testing.T) {
	root := hermesTestHome(t)

	home, err := resolveHermesHome("work")
	if err != nil {
		t.Fatalf("resolveHermesHome: %v", err)
	}
	want := filepath.Join(root, "profiles", "work")
	if home.Dir != want {
		t.Fatalf("dir = %q, want %q", home.Dir, want)
	}
	if home.IsDefault {
		t.Error("named profile must not report isDefault")
	}
	if home.ConfigPath != filepath.Join(want, "config.yaml") {
		t.Errorf("configPath = %q", home.ConfigPath)
	}
}

func TestListHermesProfiles_SkipsBareDirectories(t *testing.T) {
	root := hermesTestHome(t)

	// A directory with no identity file is not a profile — Hermes ignores it,
	// and so must we, or the picker offers a target that 404s.
	bare := filepath.Join(root, "profiles", "leftover")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeProfile(t, root, "work", "model:\n  default: \"deepseek/deepseek-chat\"\n  provider: \"custom\"\n  base_url: \"http://127.0.0.1:20130/v1\"\n")

	profiles, err := listHermesProfiles()
	if err != nil {
		t.Fatalf("listHermesProfiles: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("got %d profiles, want 2 (default + work): %+v", len(profiles), profiles)
	}
	if profiles[0].Name != "default" || !profiles[0].IsDefault {
		t.Errorf("first entry must be the default profile, got %+v", profiles[0])
	}
	if profiles[1].Command != "hermes -p work" {
		t.Errorf("command = %q, want %q", profiles[1].Command, "hermes -p work")
	}
	if !profiles[1].Has9Router {
		t.Error("a custom provider on 127.0.0.1 must report has9Router")
	}
	if profiles[1].Alias == nil || *profiles[1].Alias != "work" {
		t.Errorf("alias = %v, want work", profiles[1].Alias)
	}
}

func TestListHermesProfiles_MissingInstallReturnsDefaultOnly(t *testing.T) {
	hermesTestHome(t)

	profiles, err := listHermesProfiles()
	if err != nil {
		t.Fatalf("a missing install must not error, got %v", err)
	}
	if len(profiles) != 1 || profiles[0].Name != "default" {
		t.Fatalf("want just the default entry, got %+v", profiles)
	}
}

func TestListHermesProfiles_ReadsDisplayNameFromProfileYAML(t *testing.T) {
	root := hermesTestHome(t)
	dir := writeProfile(t, root, "work", "")
	if err := os.WriteFile(filepath.Join(dir, "profile.yaml"), []byte("display_name: \"Work Agent\"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile profile.yaml: %v", err)
	}

	profiles, err := listHermesProfiles()
	if err != nil {
		t.Fatalf("listHermesProfiles: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("got %d profiles, want 2", len(profiles))
	}
	if profiles[1].DisplayName == nil || *profiles[1].DisplayName != "Work Agent" {
		t.Errorf("displayName = %v, want %q", profiles[1].DisplayName, "Work Agent")
	}
}
